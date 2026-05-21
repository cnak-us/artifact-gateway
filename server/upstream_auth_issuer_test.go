package server

import (
	"context"
	"encoding/base64"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/store"
)

// fakeMinter satisfies IssuerMinter for tests. Counts calls so we can
// verify cache hits / forced reminting.
type fakeMinter struct {
	calls atomic.Int32
	tok   mintedToken
	err   error
}

func (f *fakeMinter) Mint(_ context.Context, _ []byte, _ []byte) (mintedToken, error) {
	f.calls.Add(1)
	return f.tok, f.err
}

// newTestCrypto builds a real auth.Crypto for tests so seal+open works.
// Mirrors how the auth package's tests construct Crypto.
func newTestCrypto(t *testing.T) *auth.Crypto {
	t.Helper()
	c, err := auth.NewCrypto(base64.StdEncoding.EncodeToString(make([]byte, 32))) // all-zeros KEK is fine for tests
	if err != nil {
		t.Fatalf("auth.NewCrypto: %v", err)
	}
	return c
}

func TestIssuerAuthCacheHitMissForceEvict(t *testing.T) {
	crypto := newTestCrypto(t)
	a := &IssuerMintAuthenticator{Crypto: crypto, minters: map[string]IssuerMinter{}}
	fm := &fakeMinter{tok: mintedToken{AuthHeader: "Bearer test-1", TTL: 10 * time.Minute}}
	a.RegisterMinter("ecr", fm)

	sealed, err := crypto.Seal([]byte(`{"accessKeyId":"x","secretAccessKey":"y"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	cred := &store.UpstreamCredential{
		ID:               uuid.New(),
		Kind:             "ecr",
		IssuerKind:       "aws",
		IssuerSecretEnc:  sealed,
		IssuerConfigJSON: []byte(`{"region":"us-east-1"}`),
	}

	// Cold cache → mint.
	got, err := a.tokenFor(context.Background(), cred, false)
	if err != nil {
		t.Fatalf("tokenFor 1: %v", err)
	}
	if got != "Bearer test-1" {
		t.Fatalf("got %q, want Bearer test-1", got)
	}
	if fm.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", fm.calls.Load())
	}

	// Cache hit → no second mint.
	got, _ = a.tokenFor(context.Background(), cred, false)
	if got != "Bearer test-1" || fm.calls.Load() != 1 {
		t.Fatalf("cache miss: got %q, calls=%d", got, fm.calls.Load())
	}

	// Forced eviction → re-mint.
	fm.tok = mintedToken{AuthHeader: "Bearer test-2", TTL: 10 * time.Minute}
	got, _ = a.tokenFor(context.Background(), cred, true)
	if got != "Bearer test-2" {
		t.Fatalf("forced mint got %q, want Bearer test-2", got)
	}
	if fm.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", fm.calls.Load())
	}
}

func TestIssuerAuthOnUnauthorizedRetries(t *testing.T) {
	crypto := newTestCrypto(t)
	a := &IssuerMintAuthenticator{Crypto: crypto, minters: map[string]IssuerMinter{}}
	fm := &fakeMinter{tok: mintedToken{AuthHeader: "Bearer fresh", TTL: time.Hour}}
	a.RegisterMinter("ecr", fm)

	sealed, _ := crypto.Seal([]byte(`{"accessKeyId":"x","secretAccessKey":"y"}`))
	cred := &store.UpstreamCredential{
		ID: uuid.New(), Kind: "ecr",
		IssuerSecretEnc:  sealed,
		IssuerConfigJSON: []byte(`{"region":"us-east-1"}`),
	}
	// Prime cache.
	_, _ = a.tokenFor(context.Background(), cred, false)
	if fm.calls.Load() != 1 {
		t.Fatalf("initial mint calls=%d, want 1", fm.calls.Load())
	}
	// OnUnauthorized should force-evict and re-mint.
	retry, err := a.OnUnauthorized(context.Background(), nil, cred, nil)
	if err != nil {
		t.Fatalf("OnUnauthorized: %v", err)
	}
	if !retry {
		t.Fatal("OnUnauthorized should request retry after successful re-mint")
	}
	if fm.calls.Load() != 2 {
		t.Fatalf("calls after eviction = %d, want 2", fm.calls.Load())
	}
}

func TestIssuerAuthMinterError(t *testing.T) {
	crypto := newTestCrypto(t)
	a := &IssuerMintAuthenticator{Crypto: crypto, minters: map[string]IssuerMinter{}}
	a.RegisterMinter("ecr", &fakeMinter{err: errors.New("cloud is down")})

	sealed, _ := crypto.Seal([]byte(`{"accessKeyId":"x","secretAccessKey":"y"}`))
	cred := &store.UpstreamCredential{
		ID: uuid.New(), Kind: "ecr",
		IssuerSecretEnc:  sealed,
		IssuerConfigJSON: []byte(`{"region":"us-east-1"}`),
	}
	_, err := a.tokenFor(context.Background(), cred, false)
	if err == nil || err.Error() != "cloud is down" {
		t.Fatalf("got %v, want cloud-is-down error", err)
	}
}

func TestEffectiveHostIssuerKinds(t *testing.T) {
	cases := []struct {
		name string
		cred *store.UpstreamCredential
		want string
	}{
		{
			"gar uses BaseURL",
			&store.UpstreamCredential{Kind: "gar", BaseURL: "https://us-docker.pkg.dev"},
			"https://us-docker.pkg.dev",
		},
		{
			"acr-aad uses BaseURL",
			&store.UpstreamCredential{Kind: "acr-aad", BaseURL: "https://myreg.azurecr.io"},
			"https://myreg.azurecr.io",
		},
		{
			"ecr derives host from issuer_config",
			&store.UpstreamCredential{
				Kind:             "ecr",
				IssuerConfigJSON: []byte(`{"accountId":"123456789012","region":"us-west-2"}`),
			},
			"https://123456789012.dkr.ecr.us-west-2.amazonaws.com",
		},
		{
			"ecr BaseURL override wins",
			&store.UpstreamCredential{
				Kind:             "ecr",
				BaseURL:          "https://override.example",
				IssuerConfigJSON: []byte(`{"accountId":"123","region":"us-east-1"}`),
			},
			"https://override.example",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveHost(tc.cred); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewIssuerMintAuthenticatorRegistersDefaults(t *testing.T) {
	a := NewIssuerMintAuthenticator(nil)
	for _, kind := range IssuerKinds {
		if _, ok := a.minters[kind]; !ok {
			t.Errorf("default minter not registered for %s", kind)
		}
	}
}
