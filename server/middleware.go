// Package server hosts artifact-gateway's HTTP layer: the OCI v2 router,
// admin REST API, and the bootstrap of the public and management listeners.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	"github.com/google/uuid"
)

// ctxKey is the unexported context key type for values stashed by middleware.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyOCIClaims
)

// RequestID ensures every request has an X-Request-ID header (incoming or
// generated) and stashes the value on the context for handlers and the logger.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFrom returns the X-Request-ID set by RequestID, or "" if none.
func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

// statusRecorder captures the response status so Logger can include it.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Logger emits a structured access-log line per request.
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", clientIP(r),
				"request_id", RequestIDFrom(r.Context()),
			)
		})
	}
}

// BearerJWT validates `Authorization: Bearer <jwt>` and stashes the claims on
// the context. On failure it returns 401 with the OCI Www-Authenticate
// challenge so docker/helm retry against /v2/token.
func BearerJWT(signer *auth.JWTSigner, cfg *config.Config) func(http.Handler) http.Handler {
	challenge := bearerChallenge(cfg.ExternalHostname, "")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
				writeBearerChallenge(w, challenge, http.StatusUnauthorized, "authentication required")
				return
			}
			token := strings.TrimSpace(h[len(prefix):])
			claims, err := signer.Verify(token)
			if err != nil {
				writeBearerChallenge(w, challenge, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyOCIClaims, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFrom returns the OCI claims attached by BearerJWT, or nil.
func ClaimsFrom(ctx context.Context) *auth.OCIClaims {
	v, _ := ctx.Value(ctxKeyOCIClaims).(*auth.OCIClaims)
	return v
}

// bearerChallenge builds the Www-Authenticate Bearer header value per the
// Docker registry v2 token spec. scope may be empty (for /v2/ root).
//
// `hostname` may have been pasted into EXTERNAL_HOSTNAME with or without a
// scheme; we strip a leading `http://` or `https://` here so the realm never
// ends up doubled (e.g. `https://https://host/...`), which Docker mis-parses
// as host=`https`, resulting in `dial tcp: lookup https: no such host`.
func bearerChallenge(hostname, scope string) string {
	hostname = strings.TrimPrefix(hostname, "https://")
	hostname = strings.TrimPrefix(hostname, "http://")
	hostname = strings.TrimRight(hostname, "/")
	v := fmt.Sprintf(`Bearer realm="https://%s/v2/token",service="%s"`, hostname, hostname)
	if scope != "" {
		v += fmt.Sprintf(`,scope="%s"`, scope)
	}
	return v
}

// writeBearerChallenge writes a 401 with the OCI errors body.
func writeBearerChallenge(w http.ResponseWriter, challenge string, status int, msg string) {
	w.Header().Set("Www-Authenticate", challenge)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"errors":[{"code":"UNAUTHORIZED","message":%q}]}`, msg)
}

// clientIP returns the best-effort remote address, preferring
// X-Forwarded-For's first entry (set by trusted ingress) and falling back to
// RemoteAddr without the port.
func clientIP(r *http.Request) string {
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
