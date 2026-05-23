package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/cnak-us/artifact-gateway/store"
)

func TestBasicAuthenticatorApply(t *testing.T) {
	a := BasicAuthenticator{}
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/v2/", nil)
	cred := &store.UpstreamCredential{Username: "alice"}
	if err := a.Apply(context.Background(), req, cred, []byte("s3cret")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
}

func TestBasicAuthenticatorOnUnauthorized(t *testing.T) {
	a := BasicAuthenticator{}
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}
	retry, err := a.OnUnauthorized(context.Background(), resp, &store.UpstreamCredential{}, nil)
	if err != nil {
		t.Fatalf("OnUnauthorized: %v", err)
	}
	if retry {
		t.Fatal("BasicAuthenticator should never request retry")
	}
}

func TestAuthenticatorForRegistry(t *testing.T) {
	if _, ok := authenticatorFor("ghcr").(*BearerExchangeAuthenticator); !ok {
		t.Errorf("ghcr should resolve to BearerExchangeAuthenticator (manifest fetches require token-exchange)")
	}
	if _, ok := authenticatorFor("oci-basic").(BasicAuthenticator); !ok {
		t.Errorf("oci-basic should resolve to BasicAuthenticator")
	}
	if _, ok := authenticatorFor("unknown-kind-xyz").(BasicAuthenticator); !ok {
		t.Errorf("unknown kinds should fall back to BasicAuthenticator")
	}
}

func TestEffectiveHost(t *testing.T) {
	cases := []struct {
		name string
		cred *store.UpstreamCredential
		want string
	}{
		{"ghcr is always ghcr.io", &store.UpstreamCredential{Kind: "ghcr"}, "https://ghcr.io"},
		{"ghcr ignores BaseURL", &store.UpstreamCredential{Kind: "ghcr", BaseURL: "https://wrong.invalid"}, "https://ghcr.io"},
		{"oci-basic uses BaseURL", &store.UpstreamCredential{Kind: "oci-basic", BaseURL: "https://gitea.example.com"}, "https://gitea.example.com"},
		{"trailing slashes stripped", &store.UpstreamCredential{Kind: "oci-basic", BaseURL: "https://gitea.example.com//"}, "https://gitea.example.com"},
		{"oci-basic with empty BaseURL resolves to empty", &store.UpstreamCredential{Kind: "oci-basic"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveHost(tc.cred); got != tc.want {
				t.Fatalf("effectiveHost = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientForReturnsSharedWhenNoTLSConfig verifies the no-allocation path:
// a credential without per-cred TLS config gets the shared Upstream.Client.
func TestClientForReturnsSharedWhenNoTLSConfig(t *testing.T) {
	u := &Upstream{
		Client:      &http.Client{},
		clientCache: newClientCache(),
	}
	cred := &store.UpstreamCredential{ID: uuid.New()}
	if got := u.clientFor(cred); got != u.Client {
		t.Fatal("expected shared Client for credential without TLS config")
	}
}

// TestClientForBuildsPerCredentialOnCABundle ensures a credential with a CA
// bundle gets a distinct http.Client cached by (ID, UpdatedAt).
func TestClientForBuildsPerCredentialOnCABundle(t *testing.T) {
	u := &Upstream{
		Client:      &http.Client{},
		clientCache: newClientCache(),
	}
	cred := &store.UpstreamCredential{ID: uuid.New(), CABundlePEM: "garbage-not-a-real-pem"}
	c1 := u.clientFor(cred)
	if c1 == u.Client {
		t.Fatal("expected per-credential client when CABundlePEM is set")
	}
	// Same credential, same UpdatedAt -> cache hit.
	c2 := u.clientFor(cred)
	if c1 != c2 {
		t.Fatal("expected cache hit for same credential")
	}
}

// TestProxyOCIBasicRoutesToBaseURL stands up a fake httptest registry and
// confirms an oci-basic credential causes Proxy() to request from the
// credential's BaseURL rather than Upstream.Host.
func TestProxyOCIBasicRoutesToBaseURL(t *testing.T) {
	var sawAuth string
	var sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cred := &store.UpstreamCredential{ID: uuid.New(), Kind: "oci-basic", Username: "u", BaseURL: srv.URL}
	host := effectiveHost(cred)
	if host != srv.URL {
		t.Fatalf("effectiveHost = %q, want %q", host, srv.URL)
	}

	// Exercise the request path directly (full Proxy() path requires the
	// store + crypto wiring; the host-routing + auth header logic is the
	// part under test here).
	req, _ := http.NewRequest(http.MethodGet, host+"/v2/foo/bar/manifests/v1", nil)
	if err := authenticatorFor(cred.Kind).Apply(context.Background(), req, cred, []byte("pw")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:pw"))
	if sawAuth != wantAuth {
		t.Fatalf("upstream saw Authorization=%q, want %q", sawAuth, wantAuth)
	}
	if sawPath != "/v2/foo/bar/manifests/v1" {
		t.Fatalf("upstream saw path=%q, want /v2/foo/bar/manifests/v1", sawPath)
	}
}
