package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cnak-us/artifact-gateway/store"
)

// BearerExchangeAuthenticator handles bucket-B registries that gate /v2/* on
// a Docker token-exchange handshake (RFC distribution: 401 with
// `WWW-Authenticate: Bearer realm=…,service=…,scope=…`, followed by a GET
// against `realm` with Basic auth to mint a short-lived JWT, then a retry
// with `Authorization: Bearer <jwt>`).
//
// Covers: Docker Hub, Quay.io, GitLab Container Registry (self-hosted and
// SaaS). The static credential stays a username + PAT; this just adds the
// realm-call + JWT cache on top.
//
// Apply checks the in-process JWT cache keyed by (credentialID, scope). On a
// hit it sets the Bearer header. On a miss it leaves Authorization unset —
// the upstream then returns 401 with the challenge, and OnUnauthorized
// performs the exchange and asks Proxy() to retry.
//
// The cache is per-process. With N replicas you mint N tokens; Docker Hub's
// rate limits and GitLab's auth endpoints handle this without complaint
// because the per-minute mint volume is one-token-per-scope-per-credential.
type BearerExchangeAuthenticator struct {
	// HTTPClient is the client used for realm calls (NOT the proxy data
	// path). When nil, a default 30s-timeout client is used. Tests inject
	// here to point realm calls at httptest servers.
	HTTPClient *http.Client

	cache sync.Map // key bearerCacheKey -> *bearerCacheEntry
}

type bearerCacheKey struct {
	CredentialID uuid.UUID
	Service      string
	Scope        string
}

type bearerCacheEntry struct {
	Token     string
	ExpiresAt time.Time
}

// safetyMargin is subtracted from the advertised expires_in so we evict
// tokens slightly before the upstream considers them dead.
const bearerSafetyMargin = 30 * time.Second

func (b *BearerExchangeAuthenticator) client() *http.Client {
	if b.HTTPClient != nil {
		return b.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Apply sets a cached bearer if one exists for any scope on this credential.
// The first request to a given repo will miss; OnUnauthorized then mints the
// scope-pinned token and Apply uses it on the retry and all subsequent
// requests until expiry.
func (b *BearerExchangeAuthenticator) Apply(_ context.Context, req *http.Request, cred *store.UpstreamCredential, _ []byte) error {
	if tok := b.pickCached(cred.ID, req.URL); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return nil
}

// pickCached returns the first un-expired cached token that matches the
// credential. Scope matching is best-effort — we use the URL path so a
// `repository:owner/repo:pull` token is picked for requests to that repo.
// Cache misses are silent; the request goes out without Authorization,
// upstream returns 401, OnUnauthorized fills in.
func (b *BearerExchangeAuthenticator) pickCached(credID uuid.UUID, u *url.URL) string {
	wantRepo := scopeFromURL(u)
	now := time.Now()
	var fallback string
	b.cache.Range(func(k, v any) bool {
		key := k.(bearerCacheKey)
		if key.CredentialID != credID {
			return true
		}
		ent := v.(*bearerCacheEntry)
		if now.After(ent.ExpiresAt) {
			b.cache.Delete(k)
			return true
		}
		if wantRepo != "" && strings.Contains(key.Scope, wantRepo) {
			fallback = ent.Token
			return false
		}
		// Keep a generic token (e.g. registry-wide) as a last-resort fallback.
		if fallback == "" {
			fallback = ent.Token
		}
		return true
	})
	return fallback
}

// scopeFromURL derives the repository-name portion of a /v2/<repo>/manifests|blobs|tags
// URL so we can look up a previously cached scope-pinned token. Returns "" for
// non-/v2 paths.
func scopeFromURL(u *url.URL) string {
	p := strings.TrimPrefix(u.Path, "/v2/")
	if p == u.Path {
		return ""
	}
	for _, sep := range []string{"/manifests/", "/blobs/", "/tags/"} {
		if i := strings.Index(p, sep); i >= 0 {
			return p[:i]
		}
	}
	return p
}

// OnUnauthorized parses the upstream's WWW-Authenticate challenge, hits the
// realm with Basic auth to mint a bearer token, caches it, and returns
// retry=true. If the response is a 401 without a Bearer challenge, the
// credential is genuinely wrong — return false.
func (b *BearerExchangeAuthenticator) OnUnauthorized(ctx context.Context, resp *http.Response, cred *store.UpstreamCredential, pat []byte) (bool, error) {
	ch := parseBearerChallenge(resp.Header.Get("Www-Authenticate"))
	if ch == nil {
		return false, nil
	}
	tok, ttl, err := b.mintToken(ctx, ch, cred, pat)
	if err != nil {
		return false, err
	}
	b.cache.Store(bearerCacheKey{
		CredentialID: cred.ID,
		Service:      ch.Service,
		Scope:        ch.Scope,
	}, &bearerCacheEntry{
		Token:     tok,
		ExpiresAt: time.Now().Add(ttl),
	})
	return true, nil
}

type parsedBearerChallenge struct {
	Realm   string
	Service string
	Scope   string
}

// parseBearerChallenge extracts the realm/service/scope params from a
// `WWW-Authenticate: Bearer …` header. Anything that doesn't look like a
// Bearer challenge returns nil.
func parseBearerChallenge(header string) *parsedBearerChallenge {
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return nil
	}
	rest := header[len("Bearer "):]
	out := &parsedBearerChallenge{}
	for _, part := range splitCSVRespectingQuotes(rest) {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch strings.ToLower(k) {
		case "realm":
			out.Realm = v
		case "service":
			out.Service = v
		case "scope":
			out.Scope = v
		}
	}
	if out.Realm == "" {
		return nil
	}
	return out
}

// splitCSVRespectingQuotes splits a string on commas that are outside of
// double-quoted segments. Used for WWW-Authenticate, which legitimately
// embeds commas inside quoted realm/service/scope values.
func splitCSVRespectingQuotes(s string) []string {
	var out []string
	var depth int
	last := 0
	for i, r := range s {
		switch r {
		case '"':
			depth ^= 1
		case ',':
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	out = append(out, s[last:])
	return out
}

// mintToken posts to the challenge's realm with Basic auth and returns the
// minted bearer token and its TTL.
func (b *BearerExchangeAuthenticator) mintToken(ctx context.Context, ch *parsedBearerChallenge, cred *store.UpstreamCredential, pat []byte) (string, time.Duration, error) {
	u, err := url.Parse(ch.Realm)
	if err != nil {
		return "", 0, fmt.Errorf("parse realm: %w", err)
	}
	q := u.Query()
	if ch.Service != "" {
		q.Set("service", ch.Service)
	}
	if ch.Scope != "" {
		q.Set("scope", ch.Scope)
	}
	// Distribution clients send client_id; not strictly required but
	// avoids 400 from some implementations (notably Quay).
	if q.Get("client_id") == "" {
		q.Set("client_id", "artifact-gateway")
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, err
	}
	if len(pat) > 0 {
		basic := base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + string(pat)))
		req.Header.Set("Authorization", "Basic "+basic)
	}
	req.Header.Set("User-Agent", "artifact-gateway/1.0")

	resp, err := b.client().Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("realm request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", 0, fmt.Errorf("realm %s returned %s: %s", u.Host, resp.Status, strings.TrimSpace(string(body)))
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", 0, fmt.Errorf("decode realm response: %w", err)
	}
	tok := body.Token
	if tok == "" {
		tok = body.AccessToken
	}
	if tok == "" {
		return "", 0, errors.New("realm returned empty token")
	}
	ttl := time.Duration(body.ExpiresIn) * time.Second
	if ttl <= 0 {
		// Distribution spec default when expires_in is omitted.
		ttl = 60 * time.Second
	}
	if ttl > bearerSafetyMargin {
		ttl -= bearerSafetyMargin
	}
	return tok, ttl, nil
}

// bearerAuthenticatorSingleton holds the package-default authenticator used
// by dockerhub/quay/gitlab Kinds. Tests can replace it via
// RegisterAuthenticator with a custom instance.
var bearerAuthenticatorSingleton = &BearerExchangeAuthenticator{}

func init() {
	RegisterAuthenticator("dockerhub", bearerAuthenticatorSingleton)
	RegisterAuthenticator("quay", bearerAuthenticatorSingleton)
	RegisterAuthenticator("gitlab", bearerAuthenticatorSingleton)
	RegisterAuthenticator("ghcr", bearerAuthenticatorSingleton)
}

// Compile-time guard: BearerExchangeAuthenticator implements UpstreamAuthenticator.
var _ UpstreamAuthenticator = (*BearerExchangeAuthenticator)(nil)
