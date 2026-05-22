package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	agoidc "github.com/cnak-us/artifact-gateway/oidc"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/metrics"
	"github.com/cnak-us/artifact-gateway/store"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// DownloadsDeps is the dependency bag for the /download/* + signed-URL surface.
type DownloadsDeps struct {
	Store    store.DataStore
	Crypto   *auth.Crypto
	Signer   *auth.JWTSigner
	GH       *GitHubReleasesClient
	GL       *GitLabReleasesClient
	Cfg      *config.Config
	Auditor  *audit.Auditor
	Sessions *agoidc.Manager // catalog (customer) session manager
	Verifier license.Verifier
	Logger   *slog.Logger
}

// isReleaseSource reports whether a package source uses the /download/*
// release-asset code path (vs the OCI /v2/* proxy path).
func isReleaseSource(s string) bool {
	return s == "github-release" || s == "gitlab-release"
}

// signedURLTTL is how long a minted /download/_signed/<token> URL is valid.
// The design doc recommends 60-120s; we pick 90s as a middle ground.
const signedURLTTL = 90 * time.Second

// listingCacheTTL bounds how often we re-fetch releases from GitHub for a
// given (package_id, tag) tuple. Short enough to surface a newly-published
// release quickly; long enough to absorb a refresh-spam customer.
const listingCacheTTL = 60 * time.Second

// MountDownloads wires the downloads routes onto r.
//
//	GET  /download/{slug}                       — JSON listing (session OR Basic)
//	GET  /download/{slug}/{tag}/{asset}         — 302 to signed CDN URL (session OR Basic)
//	POST /catalog/api/downloads/sign            — mint a short-lived signed URL (session)
//	GET  /download/_signed/{token}              — consume the signed URL (no auth)
func MountDownloads(r chi.Router, d DownloadsDeps) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	h := &downloadsHandler{
		d:     d,
		cache: newReleaseCache(),
	}

	r.Get("/download/{slug}", h.listReleases)
	// chi treats the asset as a single segment; that's correct because GH
	// asset names cannot contain slashes by construction.
	r.Get("/download/{slug}/{tag}/{asset}", h.downloadAsset)
	r.Get("/download/_signed/{token}", h.consumeSignedURL)

	r.Route("/catalog/api/downloads", func(r chi.Router) {
		r.Post("/sign", h.signDownload)
	})
}

// --- handler ----------------------------------------------------------------

type downloadsHandler struct {
	d     DownloadsDeps
	cache *releaseCache
}

// resolved identity for the customer initiating a download request.
type downloadActor struct {
	// Subject identifying the actor for audit + signed-URL `sub` claim.
	// For Basic auth this is the token_id; for session auth this is the
	// catalog session subject (currently == token_id, but defined for drift).
	Subject string
	// Underlying customer-token row (we always need it to resolve the license).
	Token *store.CustomerToken
}

// resolveActor authenticates the request via Basic OR catalog session and
// returns the actor + the license it's bound to. On failure it writes the
// response and returns ok=false.
func (h *downloadsHandler) resolveActor(w http.ResponseWriter, r *http.Request, source string) (*downloadActor, *store.License, bool) {
	ctx := r.Context()

	// Basic first — required for the CLI flow (curl).
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(authz), "basic ") {
		tokenID, secret, ok := auth.ParseBasic(authz)
		if !ok {
			h.writeUnauthorized(w, source, "malformed Basic credentials")
			return nil, nil, false
		}
		ct, err := h.d.Store.GetCustomerTokenByTokenID(ctx, tokenID)
		if err != nil {
			h.writeUnauthorized(w, source, "invalid credentials")
			return nil, nil, false
		}
		if ct.RevokedAt != nil {
			h.writeUnauthorized(w, source, "credential revoked")
			return nil, nil, false
		}
		if ct.ExpiresAt != nil && time.Now().After(*ct.ExpiresAt) {
			h.writeUnauthorized(w, source, "credential expired")
			return nil, nil, false
		}
		if err := auth.VerifySecret(ct.SecretHash, secret); err != nil {
			h.writeUnauthorized(w, source, "invalid credentials")
			return nil, nil, false
		}
		lic, ok := h.loadLicense(w, r, ct, source)
		if !ok {
			return nil, nil, false
		}
		return &downloadActor{Subject: tokenID, Token: ct}, lic, true
	}

	// Fall back to the catalog session cookie.
	if h.d.Sessions == nil {
		h.writeUnauthorized(w, source, "authentication required")
		return nil, nil, false
	}
	s, err := h.d.Sessions.Read(r)
	if err != nil || s.Role != "customer" {
		h.writeUnauthorized(w, source, "authentication required")
		return nil, nil, false
	}
	// OIDC catalog session: no customer_token row, so resolve the license via
	// the email→license_contacts mapping the catalog browse flow already uses
	// (see resolveLicenseForSession). Without this, browser-only customers can
	// list packages in the catalog but every download click 403s.
	if s.UserID == uuidNil {
		lic, ok := h.loadLicenseByContactEmail(w, r, s.Email, source)
		if !ok {
			return nil, nil, false
		}
		return &downloadActor{Subject: s.Email, Token: nil}, lic, true
	}
	ct, err := h.d.Store.GetCustomerToken(ctx, s.UserID)
	if err != nil {
		h.writeUnauthorized(w, source, "session stale")
		return nil, nil, false
	}
	lic, ok := h.loadLicense(w, r, ct, source)
	if !ok {
		return nil, nil, false
	}
	return &downloadActor{Subject: s.Email, Token: ct}, lic, true
}

// loadLicenseByContactEmail resolves an active license for an OIDC catalog
// session by looking up license_contacts. Mirrors the entitlement model the
// catalog browse endpoints use, so the download surface stays consistent:
// any email on a non-revoked, non-expired license can pull the packages
// granted to it.
func (h *downloadsHandler) loadLicenseByContactEmail(w http.ResponseWriter, r *http.Request, email, source string) (*store.License, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		writeJSONErr(w, http.StatusForbidden, "no entitled license for this session")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	licenses, err := h.d.Store.FindLicensesByContactEmail(r.Context(), email)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "license lookup failed")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	if len(licenses) == 0 {
		writeJSONErr(w, http.StatusForbidden, "no entitled license for this session")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	// Pick the oldest active license (matches resolveLicenseForSession's v1 rule).
	lic := &licenses[0]
	if lic.RevokedAt != nil {
		writeJSONErr(w, http.StatusForbidden, "license revoked")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	if lic.ExpiresAt != nil && time.Now().After(*lic.ExpiresAt) {
		writeJSONErr(w, http.StatusForbidden, "license expired")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	return lic, true
}

// loadLicense fetches and validates the license bound to ct.
func (h *downloadsHandler) loadLicense(w http.ResponseWriter, r *http.Request, ct *store.CustomerToken, source string) (*store.License, bool) {
	lic, err := h.d.Store.GetLicense(r.Context(), ct.LicenseID)
	if err != nil {
		writeJSONErr(w, http.StatusForbidden, "license unavailable")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	if lic.RevokedAt != nil {
		writeJSONErr(w, http.StatusForbidden, "license revoked")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	if lic.ExpiresAt != nil && time.Now().After(*lic.ExpiresAt) {
		writeJSONErr(w, http.StatusForbidden, "license expired")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	return lic, true
}

func (h *downloadsHandler) writeUnauthorized(w http.ResponseWriter, source, msg string) {
	w.Header().Set("Www-Authenticate", `Basic realm="artifact-gateway"`)
	writeJSONErr(w, http.StatusUnauthorized, msg)
	metrics.DownloadsTotal.WithLabelValues(source, "unauthorized").Inc()
}

// resolvePackage loads the package by slug and confirms it's a GitHub-release
// package the actor is entitled to. The "pull" action is reused for the
// entitlement check — the resolver treats it as pull|download semantics per
// the design doc.
func (h *downloadsHandler) resolvePackage(w http.ResponseWriter, r *http.Request, slug string, lic *store.License, source string) (*store.Package, bool) {
	pkg, err := h.d.Store.GetPackageBySlug(r.Context(), slug)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "package not found")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	if !isReleaseSource(pkg.Source) {
		writeJSONErr(w, http.StatusNotFound, "package is not a downloadable release")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	granted, err := h.d.Store.HasGrant(r.Context(), lic.ID, pkg.ID, "pull")
	if err != nil || !granted {
		writeJSONErr(w, http.StatusForbidden, "not entitled to this package")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return nil, false
	}
	return pkg, true
}

// listReleases serves GET /download/{slug}.
//
// Lists the live release set + assets for the package, with each asset's
// download_url pointing at GET /download/{slug}/{tag}/{asset}. Cached in
// memory for `listingCacheTTL` to keep GitHub rate limits comfortable.
func (h *downloadsHandler) listReleases(w http.ResponseWriter, r *http.Request) {
	const source = "github-release"
	slug := slugParam(r)

	actor, lic, ok := h.resolveActor(w, r, source)
	if !ok {
		return
	}
	pkg, ok := h.resolvePackage(w, r, slug, lic, source)
	if !ok {
		return
	}

	releases, err := h.fetchReleases(r.Context(), pkg)
	if err != nil {
		h.handleGitHubErr(w, source, err)
		return
	}

	type assetView struct {
		Name        string `json:"name"`
		Size        int64  `json:"size"`
		ContentType string `json:"content_type"`
		DownloadURL string `json:"download_url"`
	}
	type releaseView struct {
		Tag         string      `json:"tag"`
		Name        string      `json:"name"`
		PublishedAt string      `json:"published_at"`
		Prerelease  bool        `json:"prerelease"`
		Assets      []assetView `json:"assets"`
	}

	out := make([]releaseView, 0, len(releases))
	for _, rel := range releases {
		rv := releaseView{
			Tag:         rel.TagName,
			Name:        rel.Name,
			PublishedAt: rel.PublishedAt,
			Prerelease:  rel.Prerelease,
			Assets:      make([]assetView, 0, len(rel.Assets)),
		}
		for _, a := range rel.Assets {
			if !assetAllowed(a.Name, pkg.AssetPattern) {
				continue
			}
			rv.Assets = append(rv.Assets, assetView{
				Name:        a.Name,
				Size:        a.Size,
				ContentType: a.ContentType,
				DownloadURL: fmt.Sprintf("/download/%s/%s/%s",
					url.PathEscape(slug),
					url.PathEscape(rel.TagName),
					url.PathEscape(a.Name)),
			})
		}
		out = append(out, rv)
	}

	_ = actor // listings don't audit individually
	metrics.DownloadsTotal.WithLabelValues(source, "success").Inc()
	writeJSON(w, http.StatusOK, map[string]any{"releases": out})
}

// downloadAsset serves GET /download/{slug}/{tag}/{asset}.
//
// Auth: customer session OR Basic. Resolves the asset by name, asks GitHub
// for the signed CDN URL, and 302s the customer there. We do NOT proxy bytes.
func (h *downloadsHandler) downloadAsset(w http.ResponseWriter, r *http.Request) {
	const source = "github-release"
	slug := slugParam(r)
	tag := chi.URLParam(r, "tag")
	assetName := chi.URLParam(r, "asset")

	actor, lic, ok := h.resolveActor(w, r, source)
	if !ok {
		return
	}
	pkg, ok := h.resolvePackage(w, r, slug, lic, source)
	if !ok {
		return
	}
	h.redirectToAsset(w, r, pkg, tag, assetName, actor.Subject, source)
}

// signDownload serves POST /catalog/api/downloads/sign.
//
// Auth: customer session cookie. Validates {slug, tag, asset}, confirms the
// asset exists in the release, then mints a short-lived JWT bound to the
// download path. Returns {url, expires_in}.
func (h *downloadsHandler) signDownload(w http.ResponseWriter, r *http.Request) {
	const source = "github-release"

	// Session-only — Basic isn't accepted here.
	if h.d.Sessions == nil {
		h.writeUnauthorized(w, source, "authentication required")
		return
	}
	s, err := h.d.Sessions.Read(r)
	if err != nil || s.Role != "customer" {
		h.writeUnauthorized(w, source, "authentication required")
		return
	}

	var body struct {
		Slug  string `json:"slug"`
		Tag   string `json:"tag"`
		Asset string `json:"asset"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Slug == "" || body.Tag == "" || body.Asset == "" {
		writeJSONErr(w, http.StatusBadRequest, "slug, tag, and asset are required")
		return
	}

	// Resolve the actor's license + package + entitlement. OIDC catalog
	// sessions have no customer_token row (UserID is the nil UUID); they
	// resolve entitlement through the license_contacts email mapping, same
	// as the catalog browse + listReleases paths.
	var (
		lic *store.License
		ct  *store.CustomerToken
	)
	if s.UserID == uuidNil {
		var ok bool
		lic, ok = h.loadLicenseByContactEmail(w, r, s.Email, source)
		if !ok {
			return
		}
	} else {
		var err error
		ct, err = h.d.Store.GetCustomerToken(r.Context(), s.UserID)
		if err != nil {
			h.writeUnauthorized(w, source, "session stale")
			return
		}
		var ok bool
		lic, ok = h.loadLicense(w, r, ct, source)
		if !ok {
			return
		}
	}
	pkg, ok := h.resolvePackage(w, r, body.Slug, lic, source)
	if !ok {
		return
	}

	// Confirm the asset actually exists in the release before minting — no
	// point handing out a token that 404s.
	if _, _, err := h.resolveAsset(r.Context(), pkg, body.Tag, body.Asset); err != nil {
		h.handleGitHubErr(w, source, err)
		return
	}

	dlPath := fmt.Sprintf("/download/%s/%s/%s", body.Slug, body.Tag, body.Asset)
	subject := s.Email
	if subject == "" && ct != nil {
		subject = ct.TokenID
	}
	tok, exp, err := h.d.Signer.SignDownloadURL(subject, dlPath, signedURLTTL)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "sign url: "+err.Error())
		return
	}

	metrics.DownloadSignedURLsIssuedTotal.Inc()
	if h.d.Auditor != nil {
		h.d.Auditor.Log(audit.AuditEvent{
			Username:     subject,
			Action:       "sign-download",
			ResourceType: "package",
			ResourceName: pkg.Path,
			Details:      "ref=" + body.Tag + "/" + body.Asset,
			IPAddress:    clientIP(r),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"url":        "/download/_signed/" + tok,
		"expires_in": int(time.Until(exp).Seconds()),
	})
}

// consumeSignedURL serves GET /download/_signed/{token}.
//
// No auth header required — the JWT signature + path claim + expiry are the
// authorization. The package, tag, and asset come from the JWT's `path`
// claim, NOT from any URL parameter (defense against path forgery).
func (h *downloadsHandler) consumeSignedURL(w http.ResponseWriter, r *http.Request) {
	const source = "github-release-signed"
	tokenStr := chi.URLParam(r, "token")
	if tokenStr == "" {
		writeJSONErr(w, http.StatusBadRequest, "missing token")
		metrics.DownloadsTotal.WithLabelValues(source, "unauthorized").Inc()
		return
	}
	claims, err := h.d.Signer.VerifyDownloadURL(tokenStr)
	if err != nil {
		writeJSONErr(w, http.StatusUnauthorized, "invalid or expired token")
		metrics.DownloadsTotal.WithLabelValues(source, "unauthorized").Inc()
		return
	}

	slug, tag, assetName, ok := parseDownloadPath(claims.Path)
	if !ok {
		writeJSONErr(w, http.StatusBadRequest, "malformed path claim")
		metrics.DownloadsTotal.WithLabelValues(source, "unauthorized").Inc()
		return
	}

	pkg, err := h.d.Store.GetPackageBySlug(r.Context(), slug)
	if err != nil || !isReleaseSource(pkg.Source) {
		writeJSONErr(w, http.StatusNotFound, "package not found")
		metrics.DownloadsTotal.WithLabelValues(source, "not_entitled").Inc()
		return
	}

	h.redirectToAsset(w, r, pkg, tag, assetName, claims.Subject, source)
}

// redirectToAsset is the shared tail of downloadAsset and consumeSignedURL:
// resolve the upstream's asset, fetch its download URL, 302 the client there.
// Branches on pkg.Source to dispatch to the right vendor client.
func (h *downloadsHandler) redirectToAsset(w http.ResponseWriter, r *http.Request, pkg *store.Package, tag, assetName, actorSubject, source string) {
	asset, _, err := h.resolveAsset(r.Context(), pkg, tag, assetName)
	if err != nil {
		h.handleGitHubErr(w, source, err)
		return
	}

	cred, pat, err := h.loadCredAndPAT(r.Context(), pkg)
	if err != nil {
		writeJSONErr(w, http.StatusBadGateway, "upstream credential unavailable")
		metrics.DownloadsTotal.WithLabelValues(source, "upstream_error").Inc()
		return
	}

	var loc string
	switch pkg.Source {
	case "gitlab-release":
		loc, _, err = h.d.GL.assetURLForRelease(r.Context(), cred.BaseURL, pkg.GitHubRepo, tag, asset.ID, pat)
	default: // "github-release"
		loc, _, err = h.d.GH.AssetDownloadURL(r.Context(), pkg.GitHubRepo, asset.ID, pat)
	}
	if err != nil {
		h.handleGitHubErr(w, source, err)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, asset.Name))
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusFound)
	metrics.DownloadsTotal.WithLabelValues(source, "success").Inc()

	if h.d.Auditor != nil {
		h.d.Auditor.LogPackagePull(actorSubject, pkg.Path, tag+"/"+asset.Name, clientIP(r))
	}
}

// --- helpers ----------------------------------------------------------------

// fetchReleases returns the package's release set, going through the in-memory
// cache. Branches on pkg.Source so github-release and gitlab-release share the
// same caller-facing shape but call the right vendor client underneath.
func (h *downloadsHandler) fetchReleases(ctx context.Context, pkg *store.Package) ([]Release, error) {
	cacheKey := pkg.ID.String() + "|" + pkg.ReleasePattern
	if v, ok := h.cache.get(cacheKey); ok {
		return v, nil
	}

	cred, pat, err := h.loadCredAndPAT(ctx, pkg)
	if err != nil {
		return nil, err
	}

	var releases []Release
	pattern := strings.ToLower(strings.TrimSpace(pkg.ReleasePattern))
	switch pkg.Source {
	case "gitlab-release":
		if pattern == "latest" {
			rel, err := h.d.GL.GetRelease(ctx, cred.BaseURL, pkg.GitHubRepo, "latest", pat)
			if err != nil {
				return nil, err
			}
			releases = []Release{*rel}
		} else {
			releases, err = h.d.GL.ListReleases(ctx, cred.BaseURL, pkg.GitHubRepo, pat, 20)
			if err != nil {
				return nil, err
			}
		}
	default: // "github-release"
		if pattern == "latest" {
			rel, err := h.d.GH.GetRelease(ctx, pkg.GitHubRepo, "latest", pat)
			if err != nil {
				return nil, err
			}
			releases = []Release{*rel}
		} else {
			releases, err = h.d.GH.ListReleases(ctx, pkg.GitHubRepo, pat, 20)
			if err != nil {
				return nil, err
			}
		}
	}
	h.cache.put(cacheKey, releases)
	return releases, nil
}

// resolveAsset finds the named asset inside the named release.
func (h *downloadsHandler) resolveAsset(ctx context.Context, pkg *store.Package, tag, assetName string) (*ReleaseAsset, *Release, error) {
	cred, pat, err := h.loadCredAndPAT(ctx, pkg)
	if err != nil {
		return nil, nil, err
	}
	var rel *Release
	switch pkg.Source {
	case "gitlab-release":
		rel, err = h.d.GL.GetRelease(ctx, cred.BaseURL, pkg.GitHubRepo, tag, pat)
	default:
		rel, err = h.d.GH.GetRelease(ctx, pkg.GitHubRepo, tag, pat)
	}
	if err != nil {
		return nil, nil, err
	}
	for i := range rel.Assets {
		if rel.Assets[i].Name == assetName {
			if !assetAllowed(rel.Assets[i].Name, pkg.AssetPattern) {
				return nil, rel, errAssetNotFound
			}
			return &rel.Assets[i], rel, nil
		}
	}
	return nil, rel, errAssetNotFound
}

var errAssetNotFound = errors.New("asset not found in release")

// loadCredAndPAT returns the package's upstream credential row plus the
// decrypted PAT/token. Replaces the older loadPAT helper so callers that
// need the credential's BaseURL (gitlab-release) don't have to re-fetch it.
func (h *downloadsHandler) loadCredAndPAT(ctx context.Context, pkg *store.Package) (*store.UpstreamCredential, string, error) {
	cred, err := h.d.Store.GetUpstreamCredential(ctx, pkg.UpstreamCredentialID)
	if err != nil {
		return nil, "", fmt.Errorf("upstream credential: %w", err)
	}
	pat, err := h.d.Crypto.Open(cred.PATEnc)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt pat: %w", err)
	}
	return cred, string(pat), nil
}

// handleGitHubErr maps a GitHub client error to an HTTP response + metric.
func (h *downloadsHandler) handleGitHubErr(w http.ResponseWriter, source string, err error) {
	if errors.Is(err, errAssetNotFound) {
		writeJSONErr(w, http.StatusNotFound, "asset not found")
		metrics.DownloadsTotal.WithLabelValues(source, "upstream_error").Inc()
		return
	}
	var rl *RateLimitError
	if errors.As(err, &rl) {
		if rl.ResetUnix > 0 {
			retry := rl.ResetUnix - time.Now().Unix()
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(retry, 10))
		}
		writeJSONErr(w, http.StatusTooManyRequests, "github rate limit; retry later")
		metrics.DownloadsTotal.WithLabelValues(source, "rate_limited").Inc()
		return
	}
	var httpErr *httpError
	if errors.As(err, &httpErr) {
		if httpErr.Status == http.StatusNotFound {
			writeJSONErr(w, http.StatusNotFound, "release or asset not found")
			metrics.DownloadsTotal.WithLabelValues(source, "upstream_error").Inc()
			return
		}
	}
	h.d.Logger.Warn("github upstream error", "err", err)
	writeJSONErr(w, http.StatusBadGateway, "upstream error")
	metrics.DownloadsTotal.WithLabelValues(source, "upstream_error").Inc()
}

// assetAllowed reports whether assetName matches the package's AssetPattern.
// Empty/whitespace pattern allows everything (back-compat). Otherwise the
// pattern is split on commas; each segment is trimmed and treated as a glob
// (path.Match semantics — `*`, `?`, character classes; no `/` traversal,
// which is fine since GitHub asset names cannot contain slashes). A blank
// segment after splitting is ignored, NOT treated as "match all" — so a
// trailing comma is harmless. The asset is allowed if ANY segment matches.
func assetAllowed(assetName, pattern string) bool {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return true
	}
	for _, raw := range strings.Split(p, ",") {
		g := strings.TrimSpace(raw)
		if g == "" {
			continue
		}
		if ok, err := path.Match(g, assetName); err == nil && ok {
			return true
		}
	}
	return false
}

// parseDownloadPath extracts (slug, tag, asset) from the `/download/{slug}/{tag}/{asset}`
// path that lives in the signed-URL JWT claim. Returns ok=false on shape
// mismatch.
func parseDownloadPath(p string) (slug, tag, asset string, ok bool) {
	const prefix = "/download/"
	if !strings.HasPrefix(p, prefix) {
		return "", "", "", false
	}
	rest := p[len(prefix):]
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	// URL-decode each segment — signDownload uses raw strings, but a caller
	// could mint a token directly with percent-encoded path.
	s, err1 := url.PathUnescape(parts[0])
	t, err2 := url.PathUnescape(parts[1])
	a, err3 := url.PathUnescape(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", "", "", false
	}
	return s, t, a, true
}

// --- in-memory release listing cache ----------------------------------------

type releaseCacheEntry struct {
	releases []Release
	expires  time.Time
}

type releaseCache struct {
	mu sync.RWMutex
	m  map[string]releaseCacheEntry
}

func newReleaseCache() *releaseCache {
	return &releaseCache{m: make(map[string]releaseCacheEntry)}
}

func (c *releaseCache) get(key string) ([]Release, bool) {
	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.releases, true
}

func (c *releaseCache) put(key string, releases []Release) {
	c.mu.Lock()
	c.m[key] = releaseCacheEntry{releases: releases, expires: time.Now().Add(listingCacheTTL)}
	c.mu.Unlock()
}

// Suppress unused-var warning if license import is removed in a future edit.
var _ = uuid.Nil
var _ = license.ErrExpired
