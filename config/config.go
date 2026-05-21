// Package config provides environment-driven configuration for artifact-gateway.
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// Listeners
	PublicPort     int
	ManagementPort int

	// External hostname customers put in `docker login` — must equal the cert SAN.
	ExternalHostname string

	// Postgres
	DatabaseURL string

	// NATS (optional — disables audit publish + license cache invalidation when empty)
	NATSURL       string
	NATSCredsFile string
	NATSAuthToken string

	// Secrets
	KEKBase64         string // 32 bytes, base64, for AES-GCM envelope on stored ghcr PATs
	SessionSigningKey string // hex, HMAC-SHA256 for admin/catalog signed cookies
	JWTSigningKey     string // hex, HMAC-SHA256 for OCI bearer JWTs
	ServiceToken      string // shared secret for upstream-proxy → /api/v1 calls

	// OCI bearer JWT lifetime
	TokenTTLSeconds int

	// TLS for the public listener. In production TLS is normally terminated
	// upstream (LB / Cloudflare) and these are empty. Set both to enable
	// in-process HTTPS — useful for local dev with mkcert and for clusters
	// that mount a Kubernetes TLS Secret directly onto the pod. The
	// management listener (health + metrics) always stays HTTP.
	TLSCertFile string
	TLSKeyFile  string

	// CookieSecure sets the Secure flag on session cookies. Defaults true,
	// auto-falls-back to false when EXTERNAL_HOSTNAME points at localhost
	// (browsers silently drop Secure cookies on plain HTTP, which makes the
	// login appear to succeed but no cookie sticks).
	CookieSecure bool

	// Bootstrap admin (created at startup if no users exist)
	AdminBootstrapEmail    string
	AdminBootstrapPassword string

	// StaticAdmins is an in-memory map of email→plaintext-password loaded from
	// the STATIC_ADMINS env var. Authenticating against an entry here bypasses
	// the DB and issues an admin session with a zero UUID. Useful for break-
	// glass access, CI, or operators who don't want a real user row.
	// Format: "email1:password1,email2:password2"
	StaticAdmins map[string]string

	// OIDC
	OIDCAutoprovision   bool
	OIDCDefaultProvider string // provider name treated as the default for auto-redirect

	// Logging
	LogLevel  string
	LogFormat string

	// Kubernetes downward
	PodName string
}

func LoadFromEnv() *Config {
	return &Config{
		PublicPort:             getEnvInt("PUBLIC_PORT", 8080),
		ManagementPort:         getEnvInt("MANAGEMENT_PORT", 8090),
		ExternalHostname:       getEnv("EXTERNAL_HOSTNAME", "localhost:8080"),
		DatabaseURL:            getEnv("DATABASE_URL", ""),
		NATSURL:                getEnv("NATS_URL", ""),
		NATSCredsFile:          getEnv("NATS_CREDENTIALS_FILE", ""),
		NATSAuthToken:          getEnv("NATS_AUTH_TOKEN", ""),
		KEKBase64:              getEnv("KEK_BASE64", ""),
		SessionSigningKey:      getEnv("SESSION_SIGNING_KEY", ""),
		JWTSigningKey:          getEnv("JWT_SIGNING_KEY", ""),
		ServiceToken:           getEnv("SERVICE_TOKEN", ""),
		TokenTTLSeconds:        getEnvInt("TOKEN_TTL_SECONDS", 300),
		TLSCertFile:            getEnv("TLS_CERT_FILE", ""),
		TLSKeyFile:             getEnv("TLS_KEY_FILE", ""),
		CookieSecure:           getEnvBool("COOKIE_SECURE", defaultCookieSecure(getEnv("EXTERNAL_HOSTNAME", "localhost:8080"))),
		AdminBootstrapEmail:    getEnv("ADMIN_BOOTSTRAP_EMAIL", ""),
		AdminBootstrapPassword: getEnv("ADMIN_BOOTSTRAP_PASSWORD", ""),
		StaticAdmins:           parseStaticAdmins(getEnv("STATIC_ADMINS", "")),
		OIDCAutoprovision:      getEnvBool("OIDC_AUTOPROVISION", false),
		OIDCDefaultProvider:    getEnv("OIDC_DEFAULT_PROVIDER", "dex"),
		LogLevel:               getEnv("LOG_LEVEL", "info"),
		LogFormat:              getEnv("LOG_FORMAT", "json"),
		PodName:                getEnv("POD_NAME", ""),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

// defaultCookieSecure returns false for HTTP localhost hosts (so cookies stick
// in dev), true for everything else.
func defaultCookieSecure(externalHostname string) bool {
	h := strings.ToLower(strings.TrimSpace(externalHostname))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "http://"), "https://")
	if strings.HasPrefix(h, "localhost") || strings.HasPrefix(h, "127.") || strings.HasPrefix(h, "[::1]") {
		return false
	}
	return true
}

// parseStaticAdmins parses "email1:pw1,email2:pw2" into a map. Empty input
// returns nil. Emails are lowercased. Entries with no colon are skipped.
func parseStaticAdmins(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.Index(pair, ":")
		if idx <= 0 || idx == len(pair)-1 {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(pair[:idx]))
		password := pair[idx+1:]
		if email == "" || password == "" {
			continue
		}
		out[email] = password
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		return strings.ToLower(value) == "true" || value == "1"
	}
	return defaultValue
}
