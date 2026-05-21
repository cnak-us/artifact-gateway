package server

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cnak-us/artifact-gateway/store"
)

// clientCacheKey identifies a per-credential http.Client. UpdatedAt is part
// of the key so editing a credential (changing the CA bundle or toggling
// InsecureSkipTLSVerify) transparently invalidates the cached client.
type clientCacheKey struct {
	ID        uuid.UUID
	UpdatedAt time.Time
}

// clientCache stores per-credential http.Clients. Map is keyed by
// (credentialID, updatedAt) — a credential edit produces a new UpdatedAt
// and therefore a fresh client. The shared client at Upstream.Client is
// used whenever the credential has no per-cred TLS config.
type clientCache struct {
	mu sync.Mutex
	m  map[clientCacheKey]*http.Client
}

func newClientCache() *clientCache {
	return &clientCache{m: make(map[clientCacheKey]*http.Client)}
}

// clientFor returns an http.Client honoring the credential's TLS settings.
// If the credential has no CA bundle and TLS verification is on, the shared
// Upstream.Client is returned (no allocation). Otherwise a per-credential
// client is built (and cached) with the same timeout and no-redirect-follow
// behavior as the shared one.
func (u *Upstream) clientFor(cred *store.UpstreamCredential) *http.Client {
	if cred.CABundlePEM == "" && !cred.InsecureSkipTLSVerify {
		return u.Client
	}
	key := clientCacheKey{ID: cred.ID, UpdatedAt: cred.UpdatedAt}
	u.clientCache.mu.Lock()
	defer u.clientCache.mu.Unlock()
	if c, ok := u.clientCache.m[key]; ok {
		return c
	}

	tlsCfg := &tls.Config{InsecureSkipVerify: cred.InsecureSkipTLSVerify}
	if cred.CABundlePEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		// AppendCertsFromPEM silently ignores malformed input. We still
		// install the (possibly partial) pool — the TLS handshake will
		// surface the real error to operators if no roots match.
		pool.AppendCertsFromPEM([]byte(cred.CABundlePEM))
		tlsCfg.RootCAs = pool
	}

	c := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	u.clientCache.m[key] = c
	return c
}

// effectiveHost returns the URL host to use for a credential. Resolution
// order per Kind:
//
//   - 'ghcr'      always https://ghcr.io (pinned).
//   - 'dockerhub' always https://registry-1.docker.io (pinned).
//   - 'quay'      cred.BaseURL if set, else https://quay.io.
//   - 'ecr'       cred.BaseURL if set, else derived from issuer config.
//   - everything  cred.BaseURL (required at create time). Includes
//                 'oci-basic', 'gitlab', 'gar', 'acr-aad'.
//
// The returned value has no trailing slash.
func effectiveHost(cred *store.UpstreamCredential) string {
	var host string
	switch cred.Kind {
	case "ghcr":
		host = "https://ghcr.io"
	case "dockerhub":
		host = "https://registry-1.docker.io"
	case "quay":
		host = cred.BaseURL
		if host == "" {
			host = "https://quay.io"
		}
	case "ecr":
		host = cred.BaseURL
		if host == "" {
			host = ecrRegistryHost(cred.IssuerConfigJSON)
		}
	default:
		host = cred.BaseURL
	}
	for len(host) > 0 && host[len(host)-1] == '/' {
		host = host[:len(host)-1]
	}
	return host
}
