package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/store"
)

// IssuerMintAuthenticator handles bucket-C registries that issue short-lived
// registry tokens from a stored issuer credential (IAM keys, GCP SA JSON,
// Azure AAD client secret). Common shape across all three clouds:
//
//   1. Decrypt the issuer secret with the KEK.
//   2. Mint a short-lived registry credential via the cloud's auth API.
//   3. Cache the credential in memory, keyed by credential ID, with the
//      advertised TTL minus a safety margin.
//   4. Refresh in the background before expiry; force-evict + remint on
//      a real 401 from the registry.
//
// Per-cloud differences (region, audience, scope, exchange URL) live in
// IssuerMinter implementations registered with the authenticator.
type IssuerMintAuthenticator struct {
	Crypto *auth.Crypto

	minters map[string]IssuerMinter
	cache   sync.Map // uuid.UUID -> *issuerCacheEntry
}

// IssuerMinter mints a short-lived registry credential from a decrypted
// issuer secret. Implementations live in upstream_auth_ecr.go,
// upstream_auth_gar.go, upstream_auth_acr.go.
type IssuerMinter interface {
	// Mint returns the registry credential (typically a Basic
	// username:password tuple or a Bearer JWT) and its TTL.
	Mint(ctx context.Context, secret []byte, configJSON []byte) (mintedToken, error)
}

// mintedToken is what an IssuerMinter returns. AuthHeader is the verbatim
// value for the Authorization header (e.g. "Basic AWS:<...>" or
// "Bearer <token>"). TTL is honored by the cache.
type mintedToken struct {
	AuthHeader string
	TTL        time.Duration
}

type issuerCacheEntry struct {
	AuthHeader string
	ExpiresAt  time.Time
}

const issuerSafetyMargin = 60 * time.Second

// NewIssuerMintAuthenticator constructs an authenticator with all known
// per-cloud minters registered. Tests can override individual minters via
// the returned struct.
func NewIssuerMintAuthenticator(crypto *auth.Crypto) *IssuerMintAuthenticator {
	a := &IssuerMintAuthenticator{
		Crypto:  crypto,
		minters: map[string]IssuerMinter{},
	}
	for kind, m := range defaultIssuerMinters() {
		a.minters[kind] = m
	}
	return a
}

// RegisterMinter wires a new minter for a credential Kind. Useful for tests
// that want to substitute a fake AWS/GCP/Azure client.
func (a *IssuerMintAuthenticator) RegisterMinter(kind string, m IssuerMinter) {
	if a.minters == nil {
		a.minters = map[string]IssuerMinter{}
	}
	a.minters[kind] = m
}

// Apply looks up a cached registry credential. On a cache hit it sets the
// Authorization header; on a miss it mints synchronously (cold start /
// post-restart).
func (a *IssuerMintAuthenticator) Apply(ctx context.Context, req *http.Request, cred *store.UpstreamCredential, _ []byte) error {
	tok, err := a.tokenFor(ctx, cred, false)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", tok)
	return nil
}

// OnUnauthorized force-evicts the cached token and re-mints once. Handles
// clock-skew false-positives and credential revocations. If the second mint
// still produces a 401, the credential is genuinely wrong — return false.
func (a *IssuerMintAuthenticator) OnUnauthorized(ctx context.Context, _ *http.Response, cred *store.UpstreamCredential, _ []byte) (bool, error) {
	a.cache.Delete(cred.ID)
	if _, err := a.tokenFor(ctx, cred, true); err != nil {
		return false, err
	}
	return true, nil
}

// tokenFor returns the cached or freshly-minted Authorization header value.
// When force=true, the cache is bypassed.
func (a *IssuerMintAuthenticator) tokenFor(ctx context.Context, cred *store.UpstreamCredential, force bool) (string, error) {
	if !force {
		if v, ok := a.cache.Load(cred.ID); ok {
			ent := v.(*issuerCacheEntry)
			if time.Now().Before(ent.ExpiresAt) {
				return ent.AuthHeader, nil
			}
			a.cache.Delete(cred.ID)
		}
	}
	minter, ok := a.minters[cred.Kind]
	if !ok {
		return "", fmt.Errorf("no issuer minter registered for kind %q", cred.Kind)
	}
	if a.Crypto == nil {
		return "", errors.New("issuer-mint authenticator has no Crypto wired")
	}
	if len(cred.IssuerSecretEnc) == 0 {
		return "", fmt.Errorf("credential %q has no issuer secret", cred.Name)
	}
	secret, err := a.Crypto.Open(cred.IssuerSecretEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt issuer secret: %w", err)
	}
	mt, err := minter.Mint(ctx, secret, cred.IssuerConfigJSON)
	if err != nil {
		return "", err
	}
	ttl := mt.TTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > issuerSafetyMargin {
		ttl -= issuerSafetyMargin
	}
	a.cache.Store(cred.ID, &issuerCacheEntry{
		AuthHeader: mt.AuthHeader,
		ExpiresAt:  time.Now().Add(ttl),
	})
	return mt.AuthHeader, nil
}

// startRefreshLoop kicks off a background goroutine that periodically scans
// the cache and re-mints entries whose TTL is below the safety margin.
// Cancel via ctx. Safe to call once per Upstream construction.
func (a *IssuerMintAuthenticator) startRefreshLoop(ctx context.Context, st store.DataStore) {
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.refreshSoonToExpire(ctx, st)
			}
		}
	}()
}

func (a *IssuerMintAuthenticator) refreshSoonToExpire(ctx context.Context, st store.DataStore) {
	threshold := time.Now().Add(2 * issuerSafetyMargin)
	a.cache.Range(func(k, v any) bool {
		ent := v.(*issuerCacheEntry)
		if ent.ExpiresAt.After(threshold) {
			return true
		}
		id := k.(uuid.UUID)
		cred, err := st.GetUpstreamCredential(ctx, id)
		if err != nil {
			a.cache.Delete(id)
			return true
		}
		_, _ = a.tokenFor(ctx, cred, true)
		return true
	})
}

// Compile-time guard.
var _ UpstreamAuthenticator = (*IssuerMintAuthenticator)(nil)

// IssuerKinds lists the credential Kinds handled by IssuerMintAuthenticator.
// Used by registration code and by the admin Kind allowlist.
var IssuerKinds = []string{"ecr", "gar", "acr-aad"}

// EncodeBasic builds a Basic Authorization header value from a tuple.
// Exported helper for the minters which all use this same pattern.
func EncodeBasic(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}
