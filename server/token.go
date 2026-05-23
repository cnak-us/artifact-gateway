package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/metrics"
	"github.com/cnak-us/artifact-gateway/store"
	cnaklicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
)

// TokenHandler builds the /v2/token endpoint. It validates a customer Basic
// credential, re-verifies the bound license, intersects requested scopes with
// the license's package grants, and mints an OCI bearer JWT.
type TokenHandler struct {
	Store    store.DataStore
	Signer   *auth.JWTSigner
	Cache    *license.Cache
	Verifier license.Verifier
	Auditor  *audit.Auditor
	Cfg      *config.Config
	Logger   *slog.Logger
}

// tokenResponse is the OCI token-endpoint body. `token` is the historical
// field name; `access_token` is required by newer clients (helm).
type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	IssuedAt    string `json:"issued_at"`
}

// scopeRequest is a parsed `scope` query parameter:
//
//	repository:<name>:<action,action,...>
type scopeRequest struct {
	resourceType string
	name         string
	actions      []string
}

// ServeHTTP implements GET|POST /v2/token.
func (h *TokenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	ip := clientIP(r)
	challenge := bearerChallenge(h.Cfg.ExternalHostname, "")

	// Service param must match the configured external hostname; otherwise the
	// client is talking to the wrong registry and a successful mint would
	// produce a JWT they can't use. Normalize both sides so a docker client
	// that resends `service=https://host:port` matches an operator-configured
	// EXTERNAL_HOSTNAME of `host:port`.
	if svc := r.URL.Query().Get("service"); svc != "" {
		if normalizeService(svc) != normalizeService(h.Cfg.ExternalHostname) {
			metrics.TokenMintsTotal.WithLabelValues("unauthorized").Inc()
			writeBearerChallenge(w, challenge, http.StatusUnauthorized, "service mismatch")
			return
		}
	}

	tokenID, secret, ok := auth.ParseBasic(r.Header.Get("Authorization"))
	if !ok {
		metrics.TokenMintsTotal.WithLabelValues("unauthorized").Inc()
		writeBearerChallenge(w, challenge, http.StatusUnauthorized, "missing credentials")
		return
	}

	ctx := r.Context()
	ct, err := h.Store.GetCustomerTokenByTokenID(ctx, tokenID)
	if err != nil {
		metrics.TokenMintsTotal.WithLabelValues("unauthorized").Inc()
		h.Auditor.LogTokenMint(tokenID, "", ip, "unauthorized")
		writeBearerChallenge(w, challenge, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if ct.RevokedAt != nil {
		metrics.TokenMintsTotal.WithLabelValues("unauthorized").Inc()
		h.Auditor.LogTokenMint(tokenID, ct.LicenseID.String(), ip, "revoked")
		writeBearerChallenge(w, challenge, http.StatusUnauthorized, "credential revoked")
		return
	}
	if ct.ExpiresAt != nil && time.Now().After(*ct.ExpiresAt) {
		metrics.TokenMintsTotal.WithLabelValues("unauthorized").Inc()
		h.Auditor.LogTokenMint(tokenID, ct.LicenseID.String(), ip, "expired")
		writeBearerChallenge(w, challenge, http.StatusUnauthorized, "credential expired")
		return
	}
	if err := auth.VerifySecret(ct.SecretHash, secret); err != nil {
		metrics.TokenMintsTotal.WithLabelValues("unauthorized").Inc()
		h.Auditor.LogTokenMint(tokenID, ct.LicenseID.String(), ip, "unauthorized")
		writeBearerChallenge(w, challenge, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// License re-verification. Cache returns the parsed License; on miss we
	// parse + verify the stored blob and populate it. RevokedAt belongs to the
	// row, not the blob, so CheckActive takes it explicitly.
	lic, err := h.Store.GetLicense(ctx, ct.LicenseID)
	if err != nil {
		metrics.LicenseCheckFailuresTotal.WithLabelValues("missing").Inc()
		metrics.TokenMintsTotal.WithLabelValues("denied_license").Inc()
		h.Auditor.LogTokenMint(tokenID, ct.LicenseID.String(), ip, "denied_license")
		writeDenied(w, http.StatusForbidden, "license unavailable")
		return
	}
	parsed, ok := h.cachedLicense(lic.LicenseID, lic.LicBlob)
	if !ok {
		metrics.LicenseCheckFailuresTotal.WithLabelValues("sig_invalid").Inc()
		metrics.TokenMintsTotal.WithLabelValues("denied_license").Inc()
		h.Auditor.LogTokenMint(tokenID, lic.LicenseID, ip, "denied_license")
		writeDenied(w, http.StatusForbidden, "license invalid")
		return
	}
	if err := license.CheckActive(parsed, lic.RevokedAt, lic.LicenseID); err != nil {
		metrics.LicenseCheckFailuresTotal.WithLabelValues(licenseFailureReason(err)).Inc()
		metrics.TokenMintsTotal.WithLabelValues("denied_license").Inc()
		h.Auditor.LogTokenMint(tokenID, lic.LicenseID, ip, "denied_license")
		writeDenied(w, http.StatusForbidden, "license "+err.Error())
		return
	}

	requested := parseScopes(r.URL.Query()["scope"])
	granted := make([]auth.Access, 0, len(requested))
	for _, s := range requested {
		if s.resourceType != "repository" || len(s.actions) == 0 {
			continue
		}
		pkg, ok := resolvePackageForScope(ctx, h.Store, s.name)
		if !ok {
			// Unknown package or unknown container: drop scope silently —
			// matches Docker spec, the client will retry-or-fail with a 401
			// from the OCI handler.
			continue
		}
		allowed := make([]string, 0, len(s.actions))
		for _, a := range s.actions {
			hasGrant, err := h.Store.HasGrant(ctx, lic.ID, pkg.ID, a)
			if err == nil && hasGrant {
				allowed = append(allowed, a)
			}
		}
		if len(allowed) == 0 {
			continue
		}
		granted = append(granted, auth.Access{
			Type:    "repository",
			Name:    s.name,
			Actions: allowed,
		})
	}

	signed, expiresIn, issuedAt, err := h.Signer.Mint(tokenID, ct.ID, granted)
	if err != nil {
		h.Logger.Error("token mint failed", "err", err, "token_id", tokenID)
		metrics.TokenMintsTotal.WithLabelValues("denied_license").Inc()
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	metrics.TokenMintLatency.Observe(time.Since(start).Seconds())
	metrics.TokenMintsTotal.WithLabelValues("success").Inc()
	h.Auditor.LogTokenMint(tokenID, lic.LicenseID, ip, "success")

	go func() {
		// Don't propagate the request context — handler returns before this.
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := h.Store.TouchCustomerToken(bg, ct.ID); err != nil {
			h.Logger.Warn("touch customer token failed", "err", err, "id", ct.ID)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		Token:       signed,
		AccessToken: signed,
		ExpiresIn:   expiresIn,
		IssuedAt:    issuedAt.Format(time.RFC3339),
	})
}

// cachedLicense returns the parsed license, going through the in-process cache.
// On miss it parses + verifies the .lic blob and populates the cache.
func (h *TokenHandler) cachedLicense(id, blob string) (*cnaklicense.License, bool) {
	if h.Cache != nil {
		if l, hit := h.Cache.Get(id); hit {
			return l, true
		}
	}
	l, err := h.Verifier.VerifyLicenseBlob(blob)
	if err != nil {
		return nil, false
	}
	if h.Cache != nil {
		h.Cache.Put(id, l)
	}
	return l, true
}

// resolvePackageForScope maps a docker scope name (e.g. "cnak-platform" or
// "cnak-platform/backend") to its owning package. Single-container scopes
// match packages.path verbatim; multi-container scopes split on the last
// '/' and require both a package at the prefix and a container row at the
// suffix. Grants are always per-package, so the container only gates which
// repos exist — not which actions are allowed.
func resolvePackageForScope(ctx context.Context, st store.DataStore, name string) (*store.Package, bool) {
	if pkg, err := st.GetPackageByPath(ctx, name); err == nil {
		return pkg, true
	}
	slash := strings.LastIndex(name, "/")
	if slash <= 0 || slash == len(name)-1 {
		return nil, false
	}
	prefix, alias := name[:slash], name[slash+1:]
	pkg, err := st.GetPackageByPath(ctx, prefix)
	if err != nil {
		return nil, false
	}
	if _, err := st.GetContainer(ctx, pkg.ID, alias); err != nil {
		return nil, false
	}
	return pkg, true
}

// parseScopes turns repeated `scope=repository:<name>:pull,push` query values
// into structured scope requests.
func parseScopes(raw []string) []scopeRequest {
	out := make([]scopeRequest, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// type:name:actions — but `name` itself may contain a colon (port).
		// Per the spec, splits are left-to-right on the first and last colon.
		first := strings.IndexByte(s, ':')
		last := strings.LastIndexByte(s, ':')
		if first < 0 || last <= first {
			continue
		}
		out = append(out, scopeRequest{
			resourceType: s[:first],
			name:         s[first+1 : last],
			actions:      splitActions(s[last+1:]),
		})
	}
	return out
}

func splitActions(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// normalizeService strips scheme + trailing slash so a docker client that
// sends `service=https://host:port` matches an EXTERNAL_HOSTNAME of `host:port`.
func normalizeService(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimRight(s, "/")
}

func writeDenied(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"errors":[{"code":"DENIED","message":%q}]}`, msg)
}

func licenseFailureReason(err error) string {
	switch {
	case errors.Is(err, license.ErrExpired):
		return "expired"
	case errors.Is(err, license.ErrRevoked):
		return "revoked"
	case errors.Is(err, license.ErrInvalidSignature):
		return "sig_invalid"
	case errors.Is(err, license.ErrParse):
		return "parse_error"
	default:
		return "missing"
	}
}
