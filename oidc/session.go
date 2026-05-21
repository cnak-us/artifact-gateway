// Package oidc provides admin authentication for artifact-gateway: signed
// cookie sessions, a registry of OIDC providers loaded from the store, and
// the start/callback handlers for the authorization-code flow.
package oidc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// SessionTTL is the sliding window — every successful Read pushes
	// ExpiresAt forward by this much.
	SessionTTL = 12 * time.Hour
)

var (
	ErrNoSession      = errors.New("oidc: no session cookie present")
	ErrInvalidSession = errors.New("oidc: session cookie invalid or expired")
)

// Session is the payload encoded inside the admin or customer cookie.
//
// LicenseID and Impersonator are only meaningful for customer sessions and
// only populated when an admin uses "view as customer" to peek at the catalog
// for a specific license. When set:
//   - LicenseID pins the catalog identity to that license_id string, bypassing
//     the license_contacts email lookup in resolveLicenseForSession.
//   - Impersonator carries the admin's email so the catalog UI can surface a
//     banner (and any future audit-logged action can be attributed to the
//     admin rather than to a phantom customer).
type Session struct {
	UserID       uuid.UUID `json:"uid"`
	Email        string    `json:"email"`
	Role         string    `json:"role"`
	ExpiresAt    time.Time `json:"exp"`
	LicenseID    string    `json:"lic,omitempty"`
	Impersonator string    `json:"imp,omitempty"`
	CanAdmin     bool      `json:"ca,omitempty"`
	CanCustomer  bool      `json:"cc,omitempty"`
}

// Manager issues, reads, and validates signed session cookies. Sessions are
// stateless: the cookie body is the source of truth and is HMAC-signed under
// the configured key, so multi-replica deploys need no shared session store.
type Manager struct {
	key        []byte
	cookieName string
	secure     bool          // sets the Secure flag — must be false for HTTP localhost dev
	ttl        time.Duration // zero means use SessionTTL default
}

// NewManager builds a Manager from a hex-encoded HMAC-SHA256 signing key. The
// cookieName is what the browser receives (e.g. "ag_admin_session"). secure
// controls the Secure cookie flag (true for production HTTPS, false for HTTP
// localhost dev — browsers silently drop Secure cookies on plain HTTP).
func NewManager(signingKeyHex, cookieName string, secure bool) (*Manager, error) {
	return NewManagerWithTTL(signingKeyHex, cookieName, secure, 0)
}

// NewManagerWithTTL is like NewManager but uses ttl as the session lifetime
// when non-zero. When ttl is zero, Issue falls back to the package-level
// SessionTTL constant.
func NewManagerWithTTL(signingKeyHex, cookieName string, secure bool, ttl time.Duration) (*Manager, error) {
	if signingKeyHex == "" {
		return nil, errors.New("oidc: session signing key is empty")
	}
	key, err := hex.DecodeString(signingKeyHex)
	if err != nil {
		return nil, fmt.Errorf("oidc: session key not hex: %w", err)
	}
	if len(key) < 16 {
		return nil, errors.New("oidc: session key must be >=16 bytes")
	}
	if cookieName == "" {
		cookieName = "ag_admin_session"
	}
	return &Manager{key: key, cookieName: cookieName, secure: secure, ttl: ttl}, nil
}

// CookieName returns the cookie name this manager reads/writes.
func (m *Manager) CookieName() string { return m.cookieName }

// Issue serializes, signs, and sets the session cookie. ExpiresAt is filled
// in if zero so callers usually just construct {UserID, Email, Role}.
// When the Manager was created with a non-zero TTL via NewManagerWithTTL,
// that TTL overrides the package-level SessionTTL default.
func (m *Manager) Issue(w http.ResponseWriter, s Session) error {
	if s.ExpiresAt.IsZero() {
		ttl := SessionTTL
		if m.ttl > 0 {
			ttl = m.ttl
		}
		s.ExpiresAt = time.Now().Add(ttl).UTC()
	}
	value, err := m.encode(s)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    value,
		Path:     "/",
		Expires:  s.ExpiresAt,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Clear instructs the browser to drop the cookie.
func (m *Manager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Read returns the session embedded in r's cookie, or ErrNoSession /
// ErrInvalidSession. A valid-but-expired cookie counts as invalid.
func (m *Manager) Read(r *http.Request) (*Session, error) {
	c, err := r.Cookie(m.cookieName)
	if err != nil {
		return nil, ErrNoSession
	}
	return m.decode(c.Value)
}

// Refresh re-issues the cookie with a fresh ExpiresAt — call on every
// authenticated request to implement sliding expiry without touching the DB.
func (m *Manager) Refresh(w http.ResponseWriter, s Session) error {
	s.ExpiresAt = time.Now().Add(SessionTTL).UTC()
	return m.Issue(w, s)
}

// RequireAdmin is middleware that enforces an admin session. 401 if there is
// no/invalid session, 403 if the session exists but the role isn't admin.
func (m *Manager) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := m.Read(r)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if s.Role != "admin" {
			writeJSONError(w, http.StatusForbidden, "admin role required")
			return
		}
		// Slide the expiry quietly — only on safe-ish methods to avoid
		// adding a Set-Cookie on every blob proxy.
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			_ = m.Refresh(w, *s)
		}
		next.ServeHTTP(w, r.WithContext(WithSession(r.Context(), s)))
	})
}

// RequireAdmin is a convenience for callers that want middleware compatible
// with chi.Router.Use(...).
func RequireAdmin(m *Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return m.RequireAdmin(next) }
}

// encode payload-then-base64-then-attach-signature.
func (m *Manager) encode(s Session) (string, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	sig := m.sign([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (m *Manager) decode(raw string) (*Session, error) {
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return nil, ErrInvalidSession
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidSession
	}
	if !hmac.Equal(sig, m.sign([]byte(parts[0]))) {
		return nil, ErrInvalidSession
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidSession
	}
	var s Session
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, ErrInvalidSession
	}
	if time.Now().After(s.ExpiresAt) {
		return nil, ErrInvalidSession
	}
	return &s, nil
}

func (m *Manager) sign(payload []byte) []byte {
	h := hmac.New(sha256.New, m.key)
	h.Write(payload)
	return h.Sum(nil)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// Context plumbing.

type ctxKey int

const ctxKeySession ctxKey = 0

// WithSession returns a child context carrying s.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, ctxKeySession, s)
}

// SessionFrom returns the session attached by RequireAdmin, or nil.
func SessionFrom(ctx context.Context) *Session {
	s, _ := ctx.Value(ctxKeySession).(*Session)
	return s
}
