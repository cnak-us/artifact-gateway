package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cnak-us/artifact-gateway/store"
)

func TestParseBearerChallenge(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		want    *parsedBearerChallenge
		nilWant bool
	}{
		{
			name:   "docker hub challenge",
			header: `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"`,
			want: &parsedBearerChallenge{
				Realm:   "https://auth.docker.io/token",
				Service: "registry.docker.io",
				Scope:   "repository:library/alpine:pull",
			},
		},
		{
			name:   "scope contains comma",
			header: `Bearer realm="https://example.com/auth",service="reg",scope="repository:foo:pull,push"`,
			want: &parsedBearerChallenge{
				Realm:   "https://example.com/auth",
				Service: "reg",
				Scope:   "repository:foo:pull,push",
			},
		},
		{
			name:    "basic challenge ignored",
			header:  `Basic realm="docker"`,
			nilWant: true,
		},
		{
			name:    "no realm",
			header:  `Bearer service="x"`,
			nilWant: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBearerChallenge(tc.header)
			if tc.nilWant {
				if got != nil {
					t.Fatalf("want nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want non-nil")
			}
			if *got != *tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestScopeFromURL(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/v2/library/alpine/manifests/latest", "library/alpine"},
		{"/v2/myorg/myrepo/blobs/sha256:abc", "myorg/myrepo"},
		{"/v2/myorg/sub/myrepo/tags/list", "myorg/sub/myrepo"},
		{"/v2/", ""},
		{"/something-else", ""},
	}
	for _, tc := range cases {
		u, _ := url.Parse("https://example.com" + tc.path)
		if got := scopeFromURL(u); got != tc.want {
			t.Errorf("scopeFromURL(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestBearerExchangeFullCycle stands up a fake realm server and a fake
// registry. First /v2/ request returns 401 + challenge; OnUnauthorized
// performs the realm exchange; retry hits /v2/ again with the bearer and
// gets 200. Verifies the full bucket-B flow end-to-end.
func TestBearerExchangeFullCycle(t *testing.T) {
	var realmHits, regHits int
	var lastBasic, lastBearer string

	realmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realmHits++
		lastBasic = r.Header.Get("Authorization")
		if !strings.HasPrefix(lastBasic, "Basic ") {
			http.Error(w, "no creds", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "minted-jwt-abc",
			"expires_in": 60,
		})
	}))
	defer realmSrv.Close()

	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		regHits++
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			lastBearer = auth
			w.WriteHeader(http.StatusOK)
			return
		}
		challenge := `Bearer realm="` + realmSrv.URL + `",service="test",scope="repository:foo/bar:pull"`
		w.Header().Set("Www-Authenticate", challenge)
		http.Error(w, "auth", http.StatusUnauthorized)
	}))
	defer regSrv.Close()

	cred := &store.UpstreamCredential{ID: uuid.New(), Kind: "dockerhub", Username: "alice"}
	auth := &BearerExchangeAuthenticator{HTTPClient: regSrv.Client()}

	// First request: Apply finds no cached token, sends without auth.
	req1, _ := http.NewRequest(http.MethodGet, regSrv.URL+"/v2/foo/bar/manifests/v1", nil)
	if err := auth.Apply(context.Background(), req1, cred, []byte("pw")); err != nil {
		t.Fatalf("Apply 1: %v", err)
	}
	if req1.Header.Get("Authorization") != "" {
		t.Fatal("first Apply should leave Authorization empty (no cache)")
	}
	resp1, err := regSrv.Client().Do(req1)
	if err != nil {
		t.Fatalf("Do 1: %v", err)
	}
	if resp1.StatusCode != http.StatusUnauthorized {
		t.Fatalf("first request status = %d, want 401", resp1.StatusCode)
	}

	// OnUnauthorized: do the realm exchange.
	retry, err := auth.OnUnauthorized(context.Background(), resp1, cred, []byte("pw"))
	_ = resp1.Body.Close()
	if err != nil {
		t.Fatalf("OnUnauthorized: %v", err)
	}
	if !retry {
		t.Fatal("OnUnauthorized should request retry on a Bearer challenge")
	}
	if realmHits != 1 {
		t.Fatalf("realm should be hit once, got %d", realmHits)
	}
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:pw"))
	if lastBasic != wantBasic {
		t.Fatalf("realm saw Authorization=%q, want %q", lastBasic, wantBasic)
	}

	// Second request: Apply should pick up the cached token.
	req2, _ := http.NewRequest(http.MethodGet, regSrv.URL+"/v2/foo/bar/manifests/v1", nil)
	if err := auth.Apply(context.Background(), req2, cred, []byte("pw")); err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if got := req2.Header.Get("Authorization"); got != "Bearer minted-jwt-abc" {
		t.Fatalf("retry Authorization=%q, want %q", got, "Bearer minted-jwt-abc")
	}
	resp2, err := regSrv.Client().Do(req2)
	if err != nil {
		t.Fatalf("Do 2: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", resp2.StatusCode)
	}
	if lastBearer != "Bearer minted-jwt-abc" {
		t.Fatalf("registry saw Authorization=%q on retry", lastBearer)
	}

	// Third request: still cached.
	req3, _ := http.NewRequest(http.MethodGet, regSrv.URL+"/v2/foo/bar/manifests/v1", nil)
	_ = auth.Apply(context.Background(), req3, cred, []byte("pw"))
	if req3.Header.Get("Authorization") != "Bearer minted-jwt-abc" {
		t.Fatal("cached token not reused on third request")
	}
	if realmHits != 1 {
		t.Fatalf("realm should still have been hit once after cache hit; got %d", realmHits)
	}
	_ = regHits
}

// TestBearerExchangeExpiredEvicted verifies the LRU evicts an entry past its
// expires_at and the next Apply behaves like a cold cache.
func TestBearerExchangeExpiredEvicted(t *testing.T) {
	a := &BearerExchangeAuthenticator{}
	cred := &store.UpstreamCredential{ID: uuid.New()}
	a.cache.Store(bearerCacheKey{
		CredentialID: cred.ID,
		Scope:        "repository:foo:pull",
	}, &bearerCacheEntry{Token: "expired", ExpiresAt: time.Now().Add(-1 * time.Minute)})

	u, _ := url.Parse("https://example.com/v2/foo/manifests/v1")
	if tok := a.pickCached(cred.ID, u); tok != "" {
		t.Fatalf("expired entry returned token %q", tok)
	}
	if _, ok := a.cache.Load(bearerCacheKey{CredentialID: cred.ID, Scope: "repository:foo:pull"}); ok {
		t.Fatal("expired entry should have been evicted")
	}
}

func TestBearerAuthenticatorRegistered(t *testing.T) {
	for _, kind := range []string{"dockerhub", "quay", "gitlab"} {
		got := authenticatorFor(kind)
		if _, ok := got.(*BearerExchangeAuthenticator); !ok {
			t.Errorf("%s should resolve to *BearerExchangeAuthenticator, got %T", kind, got)
		}
	}
}

func TestEffectiveHostBearerKinds(t *testing.T) {
	cases := []struct {
		name string
		cred *store.UpstreamCredential
		want string
	}{
		{"dockerhub pinned", &store.UpstreamCredential{Kind: "dockerhub"}, "https://registry-1.docker.io"},
		{"dockerhub ignores BaseURL", &store.UpstreamCredential{Kind: "dockerhub", BaseURL: "https://nope.example"}, "https://registry-1.docker.io"},
		{"quay default", &store.UpstreamCredential{Kind: "quay"}, "https://quay.io"},
		{"quay self-hosted", &store.UpstreamCredential{Kind: "quay", BaseURL: "https://quay.acme.com"}, "https://quay.acme.com"},
		{"gitlab uses BaseURL", &store.UpstreamCredential{Kind: "gitlab", BaseURL: "https://registry.gitlab.com"}, "https://registry.gitlab.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveHost(tc.cred); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
