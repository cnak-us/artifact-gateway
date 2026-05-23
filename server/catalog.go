package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/metrics"
	agoidc "github.com/cnak-us/artifact-gateway/oidc"
	"github.com/cnak-us/artifact-gateway/store"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)


// CatalogDeps wires the customer-facing /catalog/* surface.
//
// Sessions uses a distinct cookie (ag_customer_session) from the admin
// Manager — admin and customer can coexist on the same browser.
type CatalogDeps struct {
	Store               store.DataStore
	Crypto              *auth.Crypto
	Cache               *license.Cache
	Verifier            license.Verifier
	Auditor             *audit.Auditor
	Sessions            *agoidc.Manager
	Upstream            *Upstream
	Cfg                 *config.Config
	Logger              *slog.Logger
	OIDCDefaultProvider string // provider name for is_default in public catalog listing
	// Revoker is shared with the OCI Deps so that credential rotation here
	// immediately invalidates cached JWT row-revocation state.
	Revoker *TokenRevocationChecker
}

// MountCatalog wires:
//
//	POST   /catalog/login                          Basic auth → sets cookie
//	POST   /catalog/logout                         clears cookie
//	GET    /catalog/api/me                         current customer + license
//	GET    /catalog/api/hostname                   {hostname} for install snippets
//	GET    /catalog/api/packages                   entitled packages
//	GET    /catalog/api/packages/{slug}            single package
//	GET    /catalog/api/packages/{slug}/tags       proxied /v2/<path>/tags/list
//
// All /catalog/api/* are gated by the customer session cookie.
func MountCatalog(r chi.Router, d CatalogDeps) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	r.Post("/catalog/login", handleCatalogLogin(d))
	r.Post("/catalog/logout", handleCatalogLogout(d))

	r.Route("/catalog/api", func(r chi.Router) {
		r.Use(requireCustomer(d))
		r.Get("/me", handleCatalogMe(d))
		r.Get("/hostname", handleCatalogHostname(d))
		r.Get("/license", handleCatalogDownloadLicense(d))
		r.Get("/packages", handleCatalogListPackages(d))
		r.Get("/packages/{slug}", handleCatalogGetPackage(d))
		r.Get("/packages/{slug}/tags", handleCatalogListTags(d))
		r.Get("/packages/{slug}/containers", handleCatalogListContainers(d))
		r.Get("/packages/{slug}/containers/{alias}/tags", handleCatalogListContainerTags(d))

		// Credential self-service. The rotate endpoint is gated by
		// RequireCustomHeader as defense-in-depth against CSRF — browsers
		// won't send the X-Requested-With header on cross-origin POSTs
		// without a preflight, which the server can refuse.
		r.Get("/credential", handleCatalogGetCredential(d))
		r.With(RequireCustomHeader("X-Requested-With")).
			Post("/credential/rotate", handleCatalogRotateCredential(d))
	})
}

// --- session helpers --------------------------------------------------------

// catalogSession encodes the customer identity into the agoidc.Session shape:
// UserID = customer-token UUID, Email = token_id (string), Role = "customer".
// LicenseID is stashed in the Session.Email's secondary slot via the role; we
// don't extend the Session struct because Manager is generic.
//
// Per-request we re-derive license metadata by looking up the customer token
// row, so the only state in the cookie is identity (which token).
type catalogIdentity struct {
	TokenRowID   uuid.UUID
	TokenID      string
	LicenseID    string // set by admin "view as customer" — pins the session to one license
	Impersonator string // admin email when impersonating, empty for real customers
	CanAdmin     bool   // set by Dex auto-flow when the same email is also an admin — drives the "Admin Dashboard" button on the catalog UI
}

func readCatalogSession(d CatalogDeps, r *http.Request) (*catalogIdentity, error) {
	s, err := d.Sessions.Read(r)
	if err != nil {
		return nil, err
	}
	if s.Role != "customer" {
		return nil, errors.New("not a customer session")
	}
	return &catalogIdentity{
		TokenRowID:   s.UserID,
		TokenID:      s.Email,
		LicenseID:    s.LicenseID,
		Impersonator: s.Impersonator,
		CanAdmin:     s.CanAdmin,
	}, nil
}

func requireCustomer(d CatalogDeps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := readCatalogSession(d, r)
			if err != nil {
				writeJSONErr(w, http.StatusUnauthorized, "authentication required")
				return
			}
			ctx := context.WithValue(r.Context(), catalogCtxKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// catalogCtxKey is the unexported context key for the resolved customer identity.
type catalogCtxKey struct{}

func catalogFromCtx(ctx context.Context) *catalogIdentity {
	id, _ := ctx.Value(catalogCtxKey{}).(*catalogIdentity)
	return id
}

// --- handlers ---------------------------------------------------------------

type catalogMeResp struct {
	TokenID      string `json:"token_id"`
	LicenseID    string `json:"license_id"`
	Customer     string `json:"customer,omitempty"`
	Organization string `json:"organization,omitempty"`
	Tier         string `json:"tier,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	TokenExpires string `json:"token_expires_at,omitempty"`
	Hostname     string `json:"hostname"`
	// Impersonator carries the admin's email when an admin is using "view as
	// customer". Empty for real customer sessions. The catalog UI uses this
	// to show a banner with an exit link back to /admin.
	Impersonator string `json:"impersonator,omitempty"`
	// CanAdmin is true when the signed-in user also has an admin session
	// minted alongside the customer session — the Dex auto-flow does this
	// when the email qualifies for both roles. Surfaced so the catalog UI
	// can render an "Admin Dashboard" button that switches contexts.
	CanAdmin bool `json:"can_admin,omitempty"`
	// IsLicensed mirrors len(Licenses) > 0 for cheap UI checks. The catalog
	// UI's NoLicenseGate keys off this to render the "no access" page when
	// false, hiding the catalog entirely.
	IsLicensed bool `json:"is_licensed"`
	// Licenses is the full set of licenses the session can act on:
	//   - Basic/customer-token sessions: the single license bound to the token.
	//   - Admin "view as customer": the pinned license only.
	//   - OIDC/Dex sessions: every license whose contacts include this email
	//     AND that passes license.CheckActive (signature + non-expired +
	//     non-revoked).
	// The customer-facing credential page renders one card per entry; rotate
	// requests must name one of these license ids.
	Licenses []sessionLicense `json:"licenses,omitempty"`
}

// sessionLicense is the per-license public DTO embedded in catalogMeResp.
// Fields are intentionally non-sensitive — no PAT, no contact email list.
type sessionLicense struct {
	ID           string `json:"id"`         // licenses.id (UUID) — what rotate uses
	LicenseID    string `json:"license_id"` // the cnaklic public id
	Customer     string `json:"customer,omitempty"`
	Organization string `json:"organization,omitempty"`
	Tier         string `json:"tier,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

func toSessionLicense(l *store.License) sessionLicense {
	out := sessionLicense{
		ID:           l.ID.String(),
		LicenseID:    l.LicenseID,
		Customer:     l.Customer,
		Organization: l.Organization,
		Tier:         l.Tier,
	}
	if l.ExpiresAt != nil {
		out.ExpiresAt = l.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

// handleCatalogLogin validates the same Basic credential customers use for
// `docker login`. Body-less; auth lives in the Authorization header. On
// success, sets the customer session cookie and returns the catalogMeResp.
func handleCatalogLogin(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		tokenID, secret, ok := auth.ParseBasic(r.Header.Get("Authorization"))
		if !ok {
			writeJSONErr(w, http.StatusUnauthorized, "Basic credentials required")
			return
		}

		ct, err := d.Store.GetCustomerTokenByTokenID(r.Context(), tokenID)
		if err != nil {
			metrics.TokenMintsTotal.WithLabelValues("unauthorized").Inc()
			d.Auditor.LogTokenMint(tokenID, "", ip, "denied")
			writeJSONErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if ct.RevokedAt != nil {
			writeJSONErr(w, http.StatusUnauthorized, "token revoked")
			return
		}
		if ct.ExpiresAt != nil && time.Now().After(*ct.ExpiresAt) {
			writeJSONErr(w, http.StatusUnauthorized, "token expired")
			return
		}
		if err := auth.VerifySecret(ct.SecretHash, secret); err != nil {
			metrics.TokenMintsTotal.WithLabelValues("unauthorized").Inc()
			d.Auditor.LogTokenMint(tokenID, ct.LicenseID.String(), ip, "denied")
			writeJSONErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		lic, err := d.Store.GetLicense(r.Context(), ct.LicenseID)
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "license unavailable")
			return
		}
		parsed, err := d.Verifier.VerifyLicenseBlob(lic.LicBlob)
		if err != nil {
			metrics.LicenseCheckFailuresTotal.WithLabelValues("sig_invalid").Inc()
			writeJSONErr(w, http.StatusForbidden, "license invalid")
			return
		}
		if err := license.CheckActive(parsed, lic.RevokedAt, lic.LicenseID); err != nil {
			metrics.LicenseCheckFailuresTotal.WithLabelValues(licenseFailureReason(err)).Inc()
			writeJSONErr(w, http.StatusForbidden, "license inactive: "+err.Error())
			return
		}
		// Cache the parsed license for /v2/token to short-circuit re-verification.
		if d.Cache != nil {
			d.Cache.Put(lic.LicenseID, parsed)
		}

		// Async: bump last_used_at.
		go func(id uuid.UUID) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = d.Store.TouchCustomerToken(ctx, id)
		}(ct.ID)

		if err := d.Sessions.Issue(w, agoidc.Session{
			UserID: ct.ID,
			Email:  tokenID,
			Role:   "customer",
		}); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "session issue failed")
			return
		}
		d.Auditor.LogTokenMint(tokenID, ct.LicenseID.String(), ip, "success-catalog")

		writeJSON(w, http.StatusOK, buildCatalogMe(d.Cfg, tokenID, lic, ct, parsed))
	}
}

func handleCatalogLogout(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.Sessions.Clear(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleCatalogMe(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := catalogFromCtx(r.Context())
		licenses, err := d.listLicensesForSession(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusForbidden, err.Error())
			return
		}
		// MUST NOT 403 on /me even when the user is unlicensed — the catalog
		// UI's NoLicenseGate keys off is_licensed=false and renders the
		// "contact support" page; 403 would trigger Dex re-auth loop.
		me := catalogMeResp{
			TokenID:      id.TokenID,
			Hostname:     normalizedHostname(d.Cfg.ExternalHostname),
			Impersonator: id.Impersonator,
			CanAdmin:     id.CanAdmin,
			IsLicensed:   len(licenses) > 0,
		}
		if len(licenses) == 0 {
			writeJSON(w, http.StatusOK, me)
			return
		}
		// First license is "primary" — kept for back-compat with existing
		// single-license UI fields. Multi-license clients use the full list.
		primary := &licenses[0]
		// Customer-token session: pass the ct so token_expires_at is filled.
		var ct *store.CustomerToken
		if id.TokenRowID != uuidNil {
			if got, err := d.Store.GetCustomerToken(r.Context(), id.TokenRowID); err == nil {
				ct = got
			}
		}
		me = buildCatalogMe(d.Cfg, id.TokenID, primary, ct, nil)
		me.Impersonator = id.Impersonator
		me.CanAdmin = id.CanAdmin
		me.IsLicensed = true
		me.Licenses = make([]sessionLicense, 0, len(licenses))
		for i := range licenses {
			me.Licenses = append(me.Licenses, toSessionLicense(&licenses[i]))
		}
		writeJSON(w, http.StatusOK, me)
	}
}

func handleCatalogHostname(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"hostname": normalizedHostname(d.Cfg.ExternalHostname)})
	}
}

type catalogPackageView struct {
	Slug                  string `json:"slug"`
	Path                  string `json:"path"`
	Kind                  string `json:"kind"`
	DisplayName           string `json:"display_name"`
	Description           string `json:"description,omitempty"`
	ReleaseNotesURL       string `json:"release_notes_url,omitempty"`
	InstallInstructionsMD string `json:"install_instructions_md,omitempty"`
	Source                string `json:"source,omitempty"`
	GitHubRepo            string `json:"github_repo,omitempty"`
	ReleasePattern        string `json:"release_pattern,omitempty"`
	AssetPattern          string `json:"asset_pattern,omitempty"`
}

func toCatalogPackageView(p store.Package) catalogPackageView {
	return catalogPackageView{
		Slug:                  p.Slug,
		Path:                  p.Path,
		Kind:                  p.Kind,
		DisplayName:           p.DisplayName,
		Description:           p.Description,
		ReleaseNotesURL:       p.ReleaseNotesURL,
		InstallInstructionsMD: p.InstallInstructionsMD,
		Source:                p.Source,
		GitHubRepo:            p.GitHubRepo,
		ReleasePattern:        p.ReleasePattern,
		AssetPattern:          p.AssetPattern,
	}
}

func handleCatalogListPackages(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := catalogFromCtx(r.Context())
		lic, _, err := d.resolveLicenseForSession(r.Context(), id)
		if errors.Is(err, ErrNoCatalogLicense) {
			// 403 — the catalog UI's NoLicenseGate (keyed off /me's
			// is_licensed=false) prevents this endpoint from being called for
			// real users; non-UI clients (curl, scripts) get the policy-true
			// answer instead of a misleading empty list.
			writeJSONErr(w, http.StatusForbidden, "NO_LICENSE")
			return
		}
		if err != nil {
			writeJSONErr(w, http.StatusForbidden, err.Error())
			return
		}
		pkgs, err := d.Store.GrantedPackagesForLicense(r.Context(), lic.ID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "list packages: "+err.Error())
			return
		}
		out := make([]catalogPackageView, 0, len(pkgs))
		for _, p := range pkgs {
			out = append(out, toCatalogPackageView(p))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// slugParam reads the {slug} URL parameter and percent-decodes it. Package
// slugs contain slashes (e.g. "containers/backend"); the catalog SPA
// encodes them as "%2F" in URLs, and chi preserves the encoding in
// URLParam. Without this decode, GetPackageBySlug misses on the literal
// "containers%2Fbackend" string and the handler 404s.
func slugParam(r *http.Request) string {
	raw := chi.URLParam(r, "slug")
	if dec, err := url.PathUnescape(raw); err == nil {
		return dec
	}
	return raw
}

func handleCatalogGetPackage(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := slugParam(r)
		id := catalogFromCtx(r.Context())
		lic, _, err := d.resolveLicenseForSession(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusForbidden, err.Error())
			return
		}
		pkg, err := d.Store.GetPackageBySlug(r.Context(), slug)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "package not found")
			return
		}
		// Entitlement check.
		granted, err := d.Store.HasGrant(r.Context(), lic.ID, pkg.ID, "pull")
		if err != nil || !granted {
			writeJSONErr(w, http.StatusForbidden, "not entitled to this package")
			return
		}
		writeJSON(w, http.StatusOK, toCatalogPackageView(*pkg))
	}
}

// handleCatalogListTags proxies /v2/<upstream-path>/tags/list with the stored
// PAT — same code path as the OCI proxy but called from the catalog UI
// (using the customer session cookie, not Bearer JWT) so the browser can
// render the latest 10 tags inline on the package detail page.
//
// Multi-container packages (those with rows in package_containers) have no
// single tag list to return; callers must hit /containers/{alias}/tags. This
// handler 400s in that case so the SPA error message points at the right URL.
func handleCatalogListTags(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := slugParam(r)
		id := catalogFromCtx(r.Context())
		lic, _, err := d.resolveLicenseForSession(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusForbidden, err.Error())
			return
		}
		pkg, err := d.Store.GetPackageBySlug(r.Context(), slug)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "package not found")
			return
		}
		granted, err := d.Store.HasGrant(r.Context(), lic.ID, pkg.ID, "pull")
		if err != nil || !granted {
			writeJSONErr(w, http.StatusForbidden, "not entitled to this package")
			return
		}
		containers, _ := d.Store.ListContainersForPackage(r.Context(), pkg.ID)
		if len(containers) > 0 {
			writeJSONErr(w, http.StatusBadRequest,
				"this package has multiple containers — call /containers/{alias}/tags")
			return
		}
		serveUpstreamTagList(w, r, d, pkg.UpstreamCredentialID, pkg.UpstreamRepo)
	}
}

type catalogContainerView struct {
	Alias       string `json:"alias"`
	DisplayName string `json:"display_name,omitempty"`
}

// handleCatalogListContainers returns the public (customer-facing) view of a
// package's containers: alias + display name only. The upstream_repo is
// deliberately omitted — it leaks internal registry layout.
func handleCatalogListContainers(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := slugParam(r)
		id := catalogFromCtx(r.Context())
		lic, _, err := d.resolveLicenseForSession(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusForbidden, err.Error())
			return
		}
		pkg, err := d.Store.GetPackageBySlug(r.Context(), slug)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "package not found")
			return
		}
		granted, err := d.Store.HasGrant(r.Context(), lic.ID, pkg.ID, "pull")
		if err != nil || !granted {
			writeJSONErr(w, http.StatusForbidden, "not entitled to this package")
			return
		}
		rows, err := d.Store.ListContainersForPackage(r.Context(), pkg.ID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "list containers: "+err.Error())
			return
		}
		out := make([]catalogContainerView, 0, len(rows))
		for _, c := range rows {
			out = append(out, catalogContainerView{Alias: c.Alias, DisplayName: c.DisplayName})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// handleCatalogListContainerTags returns tags for a single container under a
// multi-container package. Reuses the same bearer-exchange + semver-sort
// path as handleCatalogListTags.
func handleCatalogListContainerTags(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := slugParam(r)
		alias := strings.TrimSpace(chi.URLParam(r, "alias"))
		if alias == "" {
			writeJSONErr(w, http.StatusBadRequest, "alias required")
			return
		}
		id := catalogFromCtx(r.Context())
		lic, _, err := d.resolveLicenseForSession(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusForbidden, err.Error())
			return
		}
		pkg, err := d.Store.GetPackageBySlug(r.Context(), slug)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "package not found")
			return
		}
		granted, err := d.Store.HasGrant(r.Context(), lic.ID, pkg.ID, "pull")
		if err != nil || !granted {
			writeJSONErr(w, http.StatusForbidden, "not entitled to this package")
			return
		}
		container, err := d.Store.GetContainer(r.Context(), pkg.ID, alias)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "container not found")
			return
		}
		serveUpstreamTagList(w, r, d, pkg.UpstreamCredentialID, container.UpstreamRepo)
	}
}

// serveUpstreamTagList runs the Basic-then-Bearer tag-list proxy for one
// upstream repo, writing the JSON response (or error) to w.
func serveUpstreamTagList(w http.ResponseWriter, r *http.Request, d CatalogDeps, credID uuid.UUID, upstreamRepo string) {
	cred, err := d.Store.GetUpstreamCredential(r.Context(), credID)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "upstream credential missing")
		return
	}
	pat, err := d.Crypto.Open(cred.PATEnc)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "upstream credential decrypt failed")
		return
	}

	host := effectiveHost(cred)
	if host == "" {
		writeJSONErr(w, http.StatusInternalServerError, "no upstream host resolved for credential")
		return
	}
	upstreamURL := host + "/v2/" + upstreamRepo + "/tags/list"

	// Same Basic-then-bearer dance as the admin probe (admin_probe.go):
	// ghcr.io rejects Basic on tags/list and returns a placeholder Bearer
	// challenge whose scope we override with the actual repo scope before
	// minting. Harbor/Gitea/oci-basic registries return 2xx on the first
	// Basic call and never get to the retry.
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(cred.Username+":"+string(pat)))
	resp, err := tagListGet(r.Context(), d.Upstream.Client, upstreamURL, basic)
	if err != nil {
		writeJSONErr(w, http.StatusBadGateway, "upstream error: "+err.Error())
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		if ch := parseBearerChallenge(resp.Header.Get("Www-Authenticate")); ch != nil {
			_ = resp.Body.Close()
			ch.Scope = fmt.Sprintf("repository:%s:pull", upstreamRepo)
			bearer := &BearerExchangeAuthenticator{HTTPClient: d.Upstream.Client}
			token, _, mintErr := bearer.mintToken(r.Context(), ch, cred, []byte(pat))
			if mintErr != nil {
				writeJSONErr(w, http.StatusBadGateway, "bearer exchange failed: "+mintErr.Error())
				return
			}
			resp, err = tagListGet(r.Context(), d.Upstream.Client, upstreamURL, "Bearer "+token)
			if err != nil {
				writeJSONErr(w, http.StatusBadGateway, "retry-with-bearer failed: "+err.Error())
				return
			}
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		writeJSONErr(w, resp.StatusCode, "upstream returned "+resp.Status+": "+string(body))
		return
	}
	body, _ := io.ReadAll(resp.Body)
	var tl struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &tl); err == nil && len(tl.Tags) > 1 {
		tl.Tags = sortTagsSemverDesc(tl.Tags)
		writeJSON(w, http.StatusOK, tl)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func tagListGet(ctx context.Context, client *http.Client, url, authHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "artifact-gateway/1.0")
	return client.Do(req)
}

// --- helpers ----------------------------------------------------------------

func buildCatalogMe(cfg *config.Config, tokenID string, lic *store.License, ct *store.CustomerToken, _ any) catalogMeResp {
	out := catalogMeResp{
		TokenID:   tokenID,
		LicenseID: lic.LicenseID,
		Customer:  lic.Customer,
		Tier:      lic.Tier,
		Hostname:  normalizedHostname(cfg.ExternalHostname),
	}
	if lic.Organization != "" {
		out.Organization = lic.Organization
	}
	if lic.ExpiresAt != nil {
		out.ExpiresAt = lic.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if ct != nil && ct.ExpiresAt != nil {
		out.TokenExpires = ct.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

func normalizedHostname(s string) string {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimRight(s, "/")
}

// ErrNoCatalogLicense is returned by resolveLicenseForSession when an OIDC
// session is valid but the authenticated email isn't on any license's contact
// list. Callers that render landing pages (e.g. /catalog/api/me, package list)
// MUST treat this as "no entitlements, render empty" — not as an auth failure —
// or the catalog UI will bounce the user back to /login and trigger a Dex
// auto-reauth loop. Endpoints that genuinely require a license (downloads,
// per-package details) can still surface this as 403/404.
var ErrNoCatalogLicense = errors.New("no license is associated with this email")

// listLicensesForSession enumerates every license the session can act on.
// Used by handleCatalogMe to populate the multi-license UI and by the
// credential endpoints to verify that the caller is authorized for the
// license_id they want to rotate. License integrity (signature + active
// state) is checked here so callers can trust the returned slice without
// re-verifying.
//
// Returns an empty slice (not an error) when the session is authenticated
// but no license matches — callers decide whether that's a render-empty
// state or a 403.
func (d CatalogDeps) listLicensesForSession(ctx context.Context, id *catalogIdentity) ([]store.License, error) {
	// Customer-token session: exactly one license, the one bound to the token.
	if id.TokenRowID != uuidNil {
		ct, err := d.Store.GetCustomerToken(ctx, id.TokenRowID)
		if err != nil {
			return nil, errors.New("session stale")
		}
		lic, err := d.Store.GetLicense(ctx, ct.LicenseID)
		if err != nil {
			return nil, errors.New("license unavailable")
		}
		if !d.licenseIsActive(lic) {
			return nil, nil
		}
		return []store.License{*lic}, nil
	}
	// Admin "view as customer": pinned to one license.
	if id.LicenseID != "" {
		lic, err := d.Store.GetLicenseByLicenseID(ctx, id.LicenseID)
		if err != nil {
			return nil, errors.New("license unavailable")
		}
		if !d.licenseIsActive(lic) {
			return nil, nil
		}
		return []store.License{*lic}, nil
	}
	// OIDC session: enumerate all licenses where the email is a contact.
	email := strings.ToLower(strings.TrimSpace(id.TokenID))
	licenses, err := d.Store.FindLicensesByContactEmail(ctx, email)
	if err != nil {
		return nil, errors.New("license lookup failed")
	}
	out := make([]store.License, 0, len(licenses))
	for i := range licenses {
		if d.licenseIsActive(&licenses[i]) {
			out = append(out, licenses[i])
		}
	}
	return out, nil
}

// licenseIsActive verifies the license blob signature and runs CheckActive
// (non-revoked, non-expired). Returns false on any failure — corrupt or
// expired licenses are silently dropped from sessionLicense lists rather
// than surfacing the failure to the customer.
func (d CatalogDeps) licenseIsActive(lic *store.License) bool {
	if lic == nil {
		return false
	}
	parsed, err := d.Verifier.VerifyLicenseBlob(lic.LicBlob)
	if err != nil {
		return false
	}
	return license.CheckActive(parsed, lic.RevokedAt, lic.LicenseID) == nil
}

// resolveLicenseForSession returns the License and CustomerToken (if any) the
// current catalog session represents. For Basic/token sessions, looks up the
// customer_tokens row; for OIDC sessions (TokenRowID == uuidNil), looks up
// license_contacts by email and picks the first active license (v1 rule).
//
// Returns (lic, ct, nil) on success. ct is nil for OIDC sessions.
// Returns (nil, nil, ErrNoCatalogLicense) when the email has no associated
// license — callers decide whether to render an empty catalog or refuse.
// Returns (nil, nil, err) for genuine errors (stale token, lookup failure).
func (d CatalogDeps) resolveLicenseForSession(ctx context.Context, id *catalogIdentity) (*store.License, *store.CustomerToken, error) {
	if id.TokenRowID != uuidNil {
		ct, err := d.Store.GetCustomerToken(ctx, id.TokenRowID)
		if err != nil {
			return nil, nil, errors.New("session stale")
		}
		lic, err := d.Store.GetLicense(ctx, ct.LicenseID)
		if err != nil {
			return nil, nil, errors.New("license unavailable")
		}
		return lic, ct, nil
	}
	// Admin "view as customer": session pins a specific license_id, no need
	// to look at license_contacts at all.
	if id.LicenseID != "" {
		lic, err := d.Store.GetLicenseByLicenseID(ctx, id.LicenseID)
		if err != nil {
			return nil, nil, errors.New("license unavailable")
		}
		return lic, nil, nil
	}
	email := strings.ToLower(strings.TrimSpace(id.TokenID))
	licenses, err := d.Store.FindLicensesByContactEmail(ctx, email)
	if err != nil {
		return nil, nil, errors.New("license lookup failed")
	}
	if len(licenses) == 0 {
		return nil, nil, ErrNoCatalogLicense
	}
	if len(licenses) > 1 {
		d.Logger.Warn("contact has multiple active licenses; using oldest", "email", email, "count", len(licenses))
	}
	return &licenses[0], nil, nil
}

func handleCatalogDownloadLicense(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := catalogFromCtx(r.Context())
		lic, _, err := d.resolveLicenseForSession(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusForbidden, err.Error())
			return
		}
		if lic.LicBlob == "" {
			writeJSONErr(w, http.StatusNotFound, "no license blob on file")
			return
		}
		// Filename: prefer the customer slug, fall back to the license_id.
		slugSrc := lic.Customer
		if slugSrc == "" {
			slugSrc = lic.LicenseID
		}
		filename := safeLicenseFilename(slugSrc)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = io.WriteString(w, lic.LicBlob)
		if d.Auditor != nil {
			d.Auditor.Log(audit.AuditEvent{
				Username:     id.TokenID,
				Action:       "download-license",
				ResourceType: "license",
				ResourceName: lic.LicenseID,
				IPAddress:    clientIP(r),
			})
		}
	}
}

func safeLicenseFilename(seed string) string {
	s := strings.ToLower(seed)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			b.WriteByte(c)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "license"
	}
	if !strings.HasSuffix(out, ".lic") {
		out += ".lic"
	}
	return out
}
