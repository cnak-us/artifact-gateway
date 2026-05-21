package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"sync"

	"github.com/cnak-us/artifact-gateway/store"
)

// UpstreamAuthenticator is the per-Kind auth strategy for outbound proxy
// requests. Implementations may carry their own caches (e.g. JWT for
// bucket-B bearer-exchange registries) or mint short-lived tokens from
// stored issuer credentials (bucket C — ECR/GAR/ACR-AAD).
//
// Apply runs before every outbound request. OnUnauthorized runs at most
// once per Proxy() call when the upstream returns 401, and decides whether
// to retry the same request (typically true for bucket B/C, false for A).
type UpstreamAuthenticator interface {
	Apply(ctx context.Context, req *http.Request, cred *store.UpstreamCredential, pat []byte) error
	OnUnauthorized(ctx context.Context, resp *http.Response, cred *store.UpstreamCredential, pat []byte) (retry bool, err error)
}

// BasicAuthenticator encodes the credential's username + decrypted PAT as an
// HTTP Basic header. Used by 'ghcr' and 'oci-basic' kinds — any registry
// where a static PAT is accepted directly on /v2/* without a token-exchange
// handshake. The 401 from this Apply is a real "wrong credential" or "no
// permission" — no retry.
type BasicAuthenticator struct{}

func (BasicAuthenticator) Apply(_ context.Context, req *http.Request, cred *store.UpstreamCredential, pat []byte) error {
	basic := base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + string(pat)))
	req.Header.Set("Authorization", "Basic "+basic)
	return nil
}

func (BasicAuthenticator) OnUnauthorized(_ context.Context, _ *http.Response, _ *store.UpstreamCredential, _ []byte) (bool, error) {
	return false, nil
}

// authenticatorRegistry maps a credential Kind to its UpstreamAuthenticator.
// Phase 2 / Phase 3 register their kinds here (dockerhub, quay, gitlab, ecr,
// gar, acr-aad) without touching Proxy().
var (
	authenticatorRegistryMu sync.RWMutex
	authenticatorRegistry   = map[string]UpstreamAuthenticator{
		"ghcr":      BasicAuthenticator{},
		"oci-basic": BasicAuthenticator{},
	}
)

// RegisterAuthenticator wires a new Kind to an authenticator. Safe to call
// from package init() functions; later calls overwrite earlier ones.
func RegisterAuthenticator(kind string, a UpstreamAuthenticator) {
	authenticatorRegistryMu.Lock()
	defer authenticatorRegistryMu.Unlock()
	authenticatorRegistry[kind] = a
}

// authenticatorFor returns the authenticator for a credential Kind, falling
// back to BasicAuthenticator for unknown kinds. The fallback preserves
// pre-multi-registry behavior if a row somehow has an unrecognized Kind.
func authenticatorFor(kind string) UpstreamAuthenticator {
	authenticatorRegistryMu.RLock()
	defer authenticatorRegistryMu.RUnlock()
	if a, ok := authenticatorRegistry[kind]; ok {
		return a
	}
	return BasicAuthenticator{}
}
