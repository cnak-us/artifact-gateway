package oidc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"

	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/store"
	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ErrProviderUnknown is returned when a caller asks for a provider name that
// isn't loaded.
var ErrProviderUnknown = errors.New("oidc: provider unknown")

// configured is the cached, ready-to-use representation of a row from the
// oidc_providers table.
type configured struct {
	name     string
	row      store.OIDCProvider
	provider *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	oauth    *oauth2.Config
	// httpClient is non-nil ONLY when this provider has a discovery URL
	// override (i.e. an in-network URL distinct from the browser-visible
	// issuer URL). Callers that make outbound HTTP for this provider — e.g.
	// the OAuth code exchange and userInfo lookup — should thread this client
	// into the request context via `oauth2.HTTPClient` so the rewrite transport
	// applies.
	httpClient *http.Client
}

// Registry loads OIDCProvider rows from the store, decrypts each client
// secret with the configured KEK, and exposes a per-name lookup that returns
// a hot oauth2.Config + IDTokenVerifier pair.
//
// External code constructs one Registry per process at startup and calls
// Reload() when the admin UI mutates the table.
type Registry struct {
	store     store.DataStore
	crypto    *auth.Crypto
	publicURL string // e.g. "https://artifacts.example.com" — used to build redirect URLs
	logger    *slog.Logger

	// discoveryOverrides maps provider name → in-network discovery URL. When
	// set, the registry fetches the OIDC metadata from the override URL but
	// trusts the iss claim as the browser-visible issuer URL (the row.IssuerURL
	// field). All outbound HTTP for that provider — discovery, JWKS, token
	// exchange, userinfo — is transparently rewritten from the issuer host to
	// the discovery host via a custom transport.
	//
	// Use case: in docker-compose dev mode the browser sees Dex at
	// "http://localhost:5556" (port-forwarded from the host) but the gateway
	// container reaches Dex via its service name "http://dex:5556". The iss
	// claim of issued tokens must match what the browser sees so we can't
	// just point both at "dex:5556".
	discoveryOverrides map[string]string
	mu                 sync.RWMutex
	all                map[string]*configured
}

// NewRegistry constructs an empty Registry. Call Reload(ctx) to populate. The
// discoveryOverrides argument may be nil; when set it maps provider names
// (the slug in the URL, e.g. "dex") to an in-network URL where the gateway
// will fetch OIDC metadata + tokens. See the Registry doc comment for the
// browser-vs-server-side rationale.
func NewRegistry(st store.DataStore, crypto *auth.Crypto, publicURL string, logger *slog.Logger, discoveryOverrides map[string]string) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	if discoveryOverrides == nil {
		discoveryOverrides = map[string]string{}
	}
	return &Registry{
		store:              st,
		crypto:             crypto,
		publicURL:          publicURL,
		logger:             logger,
		discoveryOverrides: discoveryOverrides,
		all:                map[string]*configured{},
	}
}

// Reload pulls the current provider list, decrypts secrets, and builds the
// oauth2.Config + verifier for each enabled provider. Disabled rows are
// skipped. On per-row error we log and continue — one bad provider must not
// break the others.
func (r *Registry) Reload(ctx context.Context) error {
	rows, err := r.store.ListOIDCProviders(ctx)
	if err != nil {
		return fmt.Errorf("oidc: list providers: %w", err)
	}
	next := make(map[string]*configured, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		c, err := r.build(ctx, row)
		if err != nil {
			r.logger.Warn("oidc provider unusable",
				"name", row.Name, "err", err,
			)
			continue
		}
		next[row.Name] = c
	}
	r.mu.Lock()
	r.all = next
	r.mu.Unlock()
	return nil
}

func (r *Registry) build(ctx context.Context, row store.OIDCProvider) (*configured, error) {
	secret, err := r.crypto.Open(row.ClientSecretEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt client secret: %w", err)
	}

	// Discovery URL override: when this provider has an in-network discovery
	// URL distinct from the browser-visible iss, install a transport that
	// rewrites outbound requests from issuer-host → discovery-host. We then
	// hand go-oidc a ctx pre-loaded with that http.Client, and ask it to
	// fetch metadata from the browser-visible issuer URL. The transport
	// transparently redirects the underlying TCP connection.
	var httpClient *http.Client
	disc, hasOverride := r.discoveryOverrides[row.Name]
	if hasOverride && disc != "" && disc != row.IssuerURL {
		c, terr := newRewriteClient(row.IssuerURL, disc)
		if terr != nil {
			return nil, fmt.Errorf("build rewrite client: %w", terr)
		}
		httpClient = c
		ctx = gooidc.ClientContext(ctx, c)
	}

	prov, err := gooidc.NewProvider(ctx, row.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", row.IssuerURL, err)
	}
	scopes := row.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "email", "profile"}
	} else {
		// Ensure "openid" is always present.
		has := false
		for _, s := range scopes {
			if s == gooidc.ScopeOpenID {
				has = true
				break
			}
		}
		if !has {
			scopes = append([]string{gooidc.ScopeOpenID}, scopes...)
		}
	}
	cfg := &oauth2.Config{
		ClientID:     row.ClientID,
		ClientSecret: string(secret),
		Endpoint:     prov.Endpoint(),
		RedirectURL:  fmt.Sprintf("%s/api/v1/auth/oidc/%s/callback", r.publicURL, row.Name),
		Scopes:       scopes,
	}
	verifier := prov.Verifier(&gooidc.Config{ClientID: row.ClientID})
	return &configured{
		name:       row.Name,
		row:        row,
		provider:   prov,
		verifier:   verifier,
		oauth:      cfg,
		httpClient: httpClient,
	}, nil
}

// hostRewriteTransport reroutes HTTP requests for issuerHost to discoveryHost
// (preserving scheme), leaving every other host untouched. Used so the gateway
// can do its server-side OIDC chatter (discovery, JWKS, /token, /userinfo)
// over the compose-network DNS name while the browser-visible issuer URL
// stays put (id_token iss claim, redirect URLs, etc.).
type hostRewriteTransport struct {
	base         http.RoundTripper
	issuerHost   string
	discoveryURL *url.URL // scheme + host to swap in
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Host != t.issuerHost {
		return t.base.RoundTrip(req)
	}
	out := req.Clone(req.Context())
	newURL := *req.URL
	newURL.Scheme = t.discoveryURL.Scheme
	newURL.Host = t.discoveryURL.Host
	out.URL = &newURL
	out.Host = t.discoveryURL.Host
	return t.base.RoundTrip(out)
}

func newRewriteClient(issuerURL, discoveryURL string) (*http.Client, error) {
	iss, err := url.Parse(issuerURL)
	if err != nil {
		return nil, fmt.Errorf("parse issuer url: %w", err)
	}
	disc, err := url.Parse(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("parse discovery url: %w", err)
	}
	return &http.Client{
		Transport: &hostRewriteTransport{
			base:         http.DefaultTransport,
			issuerHost:   iss.Host,
			discoveryURL: disc,
		},
	}, nil
}

// HTTPClient returns the optional http.Client this provider has been built
// with. Callers that make outbound HTTP using `golang.org/x/oauth2` should
// stash this on the context under `oauth2.HTTPClient` before calling Exchange
// or UserInfo so the rewrite transport applies. Returns nil when the provider
// has no discovery override (then-default http.Client is fine).
func (c *configured) HTTPClient() *http.Client { return c.httpClient }

// Lookup returns the configured provider for name, or ErrProviderUnknown.
func (r *Registry) Lookup(name string) (*configured, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.all[name]
	if !ok {
		return nil, ErrProviderUnknown
	}
	return c, nil
}

// Names returns the configured provider names (for /api/v1/oidc-providers
// list responses and the admin login UI).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.all))
	for name := range r.all {
		out = append(out, name)
	}
	return out
}

// OAuthConfig returns the oauth2.Config for the named provider.
func (c *configured) OAuthConfig() *oauth2.Config { return c.oauth }

// Verifier returns the ID-token verifier for the named provider.
func (c *configured) Verifier() *gooidc.IDTokenVerifier { return c.verifier }

// userInfo calls the provider's UserInfo endpoint with the supplied access
// token. Returns an error if the IdP didn't advertise userinfo_endpoint in
// its discovery doc (e.g. plain-OAuth2 providers like GitHub).
func (c *configured) userInfo(ctx context.Context, tok *oauth2.Token) (*gooidc.UserInfo, error) {
	return c.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
}

// Row returns the database row backing this provider (for /api/v1 list
// responses — strip the encrypted secret before returning to clients).
func (c *configured) Row() store.OIDCProvider { return c.row }
