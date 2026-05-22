package oidc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

const (
	stateCookieName = "ag_oidc_state"
	stateTTL        = 5 * time.Minute
)

// HandlerDeps is the wiring needed by the Start/Callback handlers. The Login
// flow needs the registry, sessions, store, auditor, and a logger.
type HandlerDeps struct {
	Registry      *Registry
	Sessions      *Manager
	Store         store.DataStore
	Auditor       *audit.Auditor
	Logger        *slog.Logger
	StateKey      []byte // hex-decoded shared with sessions for state-cookie signing
	Autoprovision bool   // OIDC_AUTOPROVISION
	CookieSecure  bool   // whether to set Secure on the state cookie

	// CustomerCallback, when non-nil, is invoked from Callback whenever the
	// recovered state cookie carries Flow=="customer" — i.e. the start was
	// kicked off by the catalog UI rather than the admin UI. The hook owns
	// issuing the customer session and redirecting; the oidc package only
	// hands it the authenticated email/subject and the sanitized return_to.
	//
	// This lets the catalog package inject license-contact gating without the
	// oidc package importing license/store internals. Both flows share the
	// single registered redirect_uri (/api/v1/auth/oidc/<name>/callback) so
	// operators only register one URL per IdP.
	CustomerCallback func(w http.ResponseWriter, r *http.Request, providerName, email, subject, returnTo string)

	// PostAuthCallback, when non-nil, is invoked from Callback whenever the
	// recovered state cookie carries Flow=="auto". It receives the authenticated
	// email/subject and the sanitized return_to, and is responsible for issuing
	// the appropriate session(s) and redirecting. The current implementation in
	// main.go always issues an ag_customer_session and additionally an
	// ag_admin_session when the email is an admin (env STATIC_ADMINS, DB
	// static_admins, or users.role=="admin").
	PostAuthCallback func(w http.ResponseWriter, r *http.Request, providerName, email, subject, returnTo string)
}

// NewHandlerDeps builds HandlerDeps, decoding the hex state-signing key.
func NewHandlerDeps(reg *Registry, sm *Manager, st store.DataStore, auditor *audit.Auditor, stateKeyHex string, autoprovision, cookieSecure bool, logger *slog.Logger) (*HandlerDeps, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if stateKeyHex == "" {
		return nil, errors.New("oidc: state key is empty")
	}
	key, err := hex.DecodeString(stateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("oidc: state key not hex: %w", err)
	}
	return &HandlerDeps{
		Registry:      reg,
		Sessions:      sm,
		Store:         st,
		Auditor:       auditor,
		Logger:        logger,
		StateKey:      key,
		Autoprovision: autoprovision,
		CookieSecure:  cookieSecure,
	}, nil
}

// ExchangeAndExtractIdentity runs the post-redirect half of the auth-code flow
// without issuing a session, so callers can decide whether to mint an admin
// session, a customer session, or anything else. Validates the state cookie,
// exchanges the code, prefers id_token over UserInfo, and returns the
// authenticated email + subject. Returns a descriptive error suitable for
// direct HTTP-body display.
func (h *HandlerDeps) ExchangeAndExtractIdentity(r *http.Request, providerName string) (email, subject string, err error) {
	p, err := h.Registry.Lookup(providerName)
	if err != nil {
		return "", "", fmt.Errorf("unknown provider: %s", providerName)
	}
	cookie, err := r.Cookie(stateCookieName)
	if err != nil {
		return "", "", fmt.Errorf("missing state cookie")
	}
	state, err := h.decodeState(cookie.Value)
	if err != nil {
		return "", "", fmt.Errorf("invalid state cookie")
	}
	if state.Provider != providerName {
		return "", "", fmt.Errorf("state/provider mismatch")
	}
	if r.URL.Query().Get("state") != state.Nonce {
		return "", "", fmt.Errorf("state mismatch")
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		return "", "", fmt.Errorf("missing code")
	}

	ctx := r.Context()
	if hc := p.HTTPClient(); hc != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	}
	tok, err := p.OAuthConfig().Exchange(ctx, code)
	if err != nil {
		return "", "", fmt.Errorf("code exchange failed: %w", err)
	}

	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
	}
	if rawID, ok := tok.Extra("id_token").(string); ok && rawID != "" {
		idTok, err := p.Verifier().Verify(ctx, rawID)
		if err != nil {
			return "", "", fmt.Errorf("id token invalid: %w", err)
		}
		if err := idTok.Claims(&claims); err != nil {
			return "", "", fmt.Errorf("claims parse: %w", err)
		}
	} else {
		info, err := p.userInfo(ctx, tok)
		if err != nil {
			return "", "", fmt.Errorf("no id_token and userinfo failed: %w (use Dex for GitHub)", err)
		}
		_ = info.Claims(&claims)
		if claims.Email == "" {
			claims.Email = info.Email
		}
		if claims.Subject == "" {
			claims.Subject = info.Subject
		}
	}
	if claims.Email == "" {
		return "", "", fmt.Errorf("id token / userinfo missing email")
	}
	return strings.ToLower(claims.Email), claims.Subject, nil
}

// statePayload is what we encode in the ag_oidc_state cookie. The nonce is
// echoed back as the OAuth `state` parameter so the callback can rebind to
// the cookie. ReturnTo is where the browser is sent after a successful login.
// Flow tags which UI started the flow ("" = admin, "customer" = catalog) so
// the shared callback URL can dispatch to the right session-issuing path.
type statePayload struct {
	Nonce    string    `json:"n"`
	ReturnTo string    `json:"r"`
	Provider string    `json:"p"`
	Flow     string    `json:"f,omitempty"`
	Expires  time.Time `json:"e"`
}

// Start kicks off the admin auth-code flow for providerName.
//
//	GET /api/v1/auth/oidc/:provider/start[?return_to=/admin]
func (h *HandlerDeps) Start(w http.ResponseWriter, r *http.Request, providerName string) {
	h.startFlow(w, r, providerName, "")
}

// StartCustomer kicks off the customer auth-code flow for providerName. The
// only difference from Start is the Flow tag embedded in the state cookie so
// the shared callback (under /api/v1/auth/oidc/) can route the successful
// exchange to the CustomerCallback hook instead of issuing an admin session.
//
//	GET /catalog/oidc/:provider/start[?return_to=/catalog]
func (h *HandlerDeps) StartCustomer(w http.ResponseWriter, r *http.Request, providerName string) {
	h.startFlow(w, r, providerName, "customer")
}

// StartAuto kicks off the auto-detect auth-code flow for providerName. The
// state cookie is tagged with Flow="auto" so the shared callback dispatches
// to PostAuthCallback, which always issues a customer session and additionally
// issues an admin session (and redirects to /admin) when the email is an
// admin. Non-admin users land on /catalog.
//
//	GET /api/v1/auth/oidc/:provider/start?flow=auto[&return_to=/catalog]
func (h *HandlerDeps) StartAuto(w http.ResponseWriter, r *http.Request, providerName string) {
	h.startFlow(w, r, providerName, "auto")
}

func (h *HandlerDeps) startFlow(w http.ResponseWriter, r *http.Request, providerName, flow string) {
	p, err := h.Registry.Lookup(providerName)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	payload := statePayload{
		Nonce:    nonce,
		ReturnTo: sanitizeReturnTo(r.URL.Query().Get("return_to"), flow),
		Provider: providerName,
		Flow:     flow,
		Expires:  time.Now().Add(stateTTL).UTC(),
	}
	value, err := h.encodeState(payload)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    value,
		Path:     "/api/v1/auth/oidc/",
		Expires:  payload.Expires,
		MaxAge:   int(stateTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.OAuthConfig().AuthCodeURL(nonce), http.StatusFound)
}

// Callback finishes the auth-code flow.
//
//	GET /api/v1/auth/oidc/:provider/callback?code=...&state=...
func (h *HandlerDeps) Callback(w http.ResponseWriter, r *http.Request, providerName string) {
	ip := remoteIP(r)
	p, err := h.Registry.Lookup(providerName)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}

	// Recover and verify state.
	cookie, err := r.Cookie(stateCookieName)
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}
	state, err := h.decodeState(cookie.Value)
	if err != nil {
		http.Error(w, "invalid state cookie", http.StatusBadRequest)
		return
	}
	if state.Provider != providerName {
		http.Error(w, "state/provider mismatch", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("state") != state.Nonce {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	// Clear the state cookie now that we've consumed it.
	http.SetCookie(w, &http.Cookie{
		Name:   stateCookieName,
		Value:  "",
		Path:   "/api/v1/auth/oidc/",
		MaxAge: -1,
	})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	// In compose mode the gateway reaches Dex via the in-network DNS name
	// while the browser-visible iss claim stays http://localhost:5556. If the
	// configured provider has a rewrite-transport http.Client attached, thread
	// it onto the context under oauth2.HTTPClient so the code exchange POSTs
	// over the compose network rather than the gateway's own loopback.
	ctx := r.Context()
	if hc := p.HTTPClient(); hc != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	}
	tok, err := p.OAuthConfig().Exchange(ctx, code)
	if err != nil {
		h.recordLogin("", providerName, ip, "exchange_failed")
		http.Error(w, "code exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
	}

	// Prefer the OIDC id_token if present. Otherwise fall back to the
	// provider's UserInfo endpoint (works for Dex and other OAuth2-shaped
	// IdPs whose discovery doc advertises userinfo_endpoint).
	//
	// GitHub is intentionally NOT supported here: it is a plain OAuth2
	// provider with no OIDC discovery and no userinfo_endpoint. Use Dex (or
	// any other OIDC bridge) in front of GitHub if you need it — the gateway
	// only speaks OIDC.
	if rawID, ok := tok.Extra("id_token").(string); ok && rawID != "" {
		idTok, err := p.Verifier().Verify(ctx, rawID)
		if err != nil {
			h.recordLogin("", providerName, ip, "id_token_invalid")
			http.Error(w, "id token invalid: "+err.Error(), http.StatusBadGateway)
			return
		}
		if err := idTok.Claims(&claims); err != nil {
			h.recordLogin("", providerName, ip, "claims_parse")
			http.Error(w, "claims parse failed: "+err.Error(), http.StatusBadGateway)
			return
		}
	} else {
		info, err := p.userInfo(ctx, tok)
		if err != nil {
			h.recordLogin("", providerName, ip, "no_id_token_no_userinfo")
			http.Error(w,
				"provider returned no id_token and userinfo lookup failed: "+err.Error()+
					" — this gateway speaks OIDC; for GitHub put Dex in front",
				http.StatusBadGateway)
			return
		}
		claims.Subject = info.Subject
		claims.Email = info.Email
		_ = info.Claims(&claims) // best-effort merge of optional fields (name, etc.)
	}
	if claims.Email == "" {
		h.recordLogin("", providerName, ip, "no_email")
		http.Error(w, "id token missing email", http.StatusBadGateway)
		return
	}
	email := strings.ToLower(claims.Email)

	// Auto flow: hand off to the PostAuthCallback which always issues a
	// customer session and additionally an admin session when the email is
	// an admin. Non-admin users land on /catalog.
	if state.Flow == "auto" {
		if h.PostAuthCallback == nil {
			h.recordLogin(email, providerName, ip, "post_auth_callback_unconfigured")
			http.Error(w, "auto login is not enabled on this gateway", http.StatusServiceUnavailable)
			return
		}
		h.PostAuthCallback(w, r, providerName, email, claims.Subject, state.ReturnTo)
		return
	}

	// Customer flow: hand off to the catalog-installed hook. The hook owns
	// the license-contact allowlist check, customer-session issuance, and the
	// final redirect to state.ReturnTo (defaulting to /catalog).
	if state.Flow == "customer" {
		if h.CustomerCallback == nil {
			h.recordLogin(email, providerName, ip, "customer_callback_unconfigured")
			http.Error(w, "customer login is not enabled on this gateway", http.StatusServiceUnavailable)
			return
		}
		h.CustomerCallback(w, r, providerName, email, claims.Subject, state.ReturnTo)
		return
	}

	user, err := h.resolveUser(ctx, p, claims.Subject, email)
	if err != nil {
		h.recordLogin(email, providerName, ip, "user_resolve_failed")
		http.Error(w, "user lookup failed", http.StatusInternalServerError)
		return
	}
	if user == nil {
		h.recordLogin(email, providerName, ip, "user_not_provisioned")
		http.Error(w, "user not provisioned — contact your administrator", http.StatusForbidden)
		return
	}
	if user.DisabledAt != nil {
		h.recordLogin(email, providerName, ip, "disabled")
		http.Error(w, "account disabled", http.StatusForbidden)
		return
	}

	if err := h.Sessions.Issue(w, Session{UserID: user.ID, Email: user.Email, Role: user.Role}); err != nil {
		h.recordLogin(email, providerName, ip, "session_issue_failed")
		http.Error(w, "session issue failed", http.StatusInternalServerError)
		return
	}
	h.recordLogin(email, providerName, ip, "success")
	http.Redirect(w, r, state.ReturnTo, http.StatusFound)
}

// resolveUser returns the existing user for (providerID, subject) or for
// email, autoprovisioning a viewer if configured.
func (h *HandlerDeps) resolveUser(ctx context.Context, p *configured, subject, email string) (*store.User, error) {
	if subject != "" {
		u, err := h.Store.GetUserByOIDC(ctx, p.row.ID, subject)
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	u, err := h.Store.GetUserByEmail(ctx, email)
	if err == nil {
		// First-time OIDC bind for an existing local user.
		pid := p.row.ID
		u.OIDCSubject = subject
		u.OIDCProviderID = &pid
		if err := h.Store.UpdateUser(ctx, u); err != nil {
			return nil, err
		}
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if !h.Autoprovision {
		return nil, nil
	}
	pid := p.row.ID
	nu := &store.User{
		ID:             uuid.New(),
		Email:          email,
		OIDCSubject:    subject,
		OIDCProviderID: &pid,
		Role:           "viewer",
	}
	if err := h.Store.InsertUser(ctx, nu); err != nil {
		return nil, err
	}
	return nu, nil
}

// encodeState marshals + signs a statePayload.
func (h *HandlerDeps) encodeState(p statePayload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, h.StateKey)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

func (h *HandlerDeps) decodeState(v string) (*statePayload, error) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed state cookie")
	}
	mac := hmac.New(sha256.New, h.StateKey)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return nil, errors.New("state cookie signature mismatch")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var p statePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if time.Now().After(p.Expires) {
		return nil, errors.New("state cookie expired")
	}
	return &p, nil
}

func (h *HandlerDeps) recordLogin(email, provider, ip, status string) {
	if h.Auditor == nil {
		return
	}
	h.Auditor.LogAdminLogin(email, "oidc:"+provider, ip, status)
}

// sanitizeReturnTo prevents open-redirect: only same-origin relative paths.
// The flow-specific default (admin → /admin, customer → /catalog,
// auto → /catalog) is used whenever the supplied value is missing or rejected.
// Any value beginning with "/choose" is also rejected (the role-chooser page
// is being removed and must never be reachable post-auth).
func sanitizeReturnTo(v, flow string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/choose") {
		switch flow {
		case "customer":
			return "/catalog"
		case "auto":
			return "/catalog"
		default:
			return "/admin"
		}
	}
	return v
}

func remoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}
