package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
		lic, ct, err := d.resolveLicenseForSession(r.Context(), id)
		if errors.Is(err, ErrNoCatalogLicense) {
			// Dex authenticated this user but they have no license entitlement.
			// Return a valid /me so the catalog UI renders an empty state instead
			// of treating 403 as "session expired" and looping back through Dex.
			writeJSON(w, http.StatusOK, catalogMeResp{
				TokenID:      id.TokenID,
				Hostname:     normalizedHostname(d.Cfg.ExternalHostname),
				Impersonator: id.Impersonator,
				CanAdmin:     id.CanAdmin,
			})
			return
		}
		if err != nil {
			writeJSONErr(w, http.StatusForbidden, err.Error())
			return
		}
		parsed, _ := d.Verifier.VerifyLicenseBlob(lic.LicBlob)
		me := buildCatalogMe(d.Cfg, id.TokenID, lic, ct, parsed)
		me.Impersonator = id.Impersonator
		me.CanAdmin = id.CanAdmin
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
			// No entitlements yet — render an empty list rather than 403,
			// matching the "Dex authenticates everyone, license gates content"
			// model. See handleCatalogMe for the parallel treatment.
			writeJSON(w, http.StatusOK, []catalogPackageView{})
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

func handleCatalogGetPackage(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := chi.URLParam(r, "slug")
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
func handleCatalogListTags(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := chi.URLParam(r, "slug")
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

		cred, err := d.Store.GetUpstreamCredential(r.Context(), pkg.UpstreamCredentialID)
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
		upstreamURL := host + "/v2/" + pkg.UpstreamRepo + "/tags/list"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "build upstream request: "+err.Error())
			return
		}
		req.SetBasicAuth(cred.Username, string(pat))
		req.Header.Set("Accept", "application/json")

		resp, err := d.Upstream.Client.Do(req)
		if err != nil {
			writeJSONErr(w, http.StatusBadGateway, "upstream error: "+err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			writeJSONErr(w, resp.StatusCode, "upstream returned "+resp.Status+": "+string(body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, resp.Body)
	}
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
