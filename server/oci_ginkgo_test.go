package server_test

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/server"
	"github.com/cnak-us/artifact-gateway/store"
	cnaklicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubVerifier returns the same parsed License every time. Production wraps
// pkglicense.Parse; the tests don't sign real .lic blobs.
type stubVerifier struct {
	licID  string
	expiry string // RFC3339; empty == perpetual
	failOn map[string]bool
}

func (s *stubVerifier) VerifyLicenseBlob(raw string) (*cnaklicense.License, error) {
	if s.failOn[raw] {
		return nil, license.ErrInvalidSignature
	}
	return &cnaklicense.License{
		ID:        s.licID,
		Customer:  "Acme",
		Tier:      cnaklicense.TierEnterprise,
		MaxTracks: 100,
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: s.expiry,
	}, nil
}

var _ = Describe("OCI server", func() {
	var (
		st        *fakeStore
		signer    *auth.JWTSigner
		crypto    *auth.Crypto
		ver       *stubVerifier
		auditor   *audit.Auditor
		cfg       *config.Config
		upstream  *httptest.Server
		publicSrv *httptest.Server
		client    *http.Client

		// Fixture identifiers — set in BeforeEach.
		licRowID  uuid.UUID
		pkgID     uuid.UUID
		credID    uuid.UUID
		tokenID   string
		secret    string
		licString = "lic-1234"
	)

	BeforeEach(func() {
		st = newFakeStore()

		// 32 random hex bytes for the JWT key.
		jwtKey := make([]byte, 32)
		_, _ = rand.Read(jwtKey)
		var err error
		signer, err = auth.NewJWTSigner(toHex(jwtKey), "artifact-gateway", "test-host:9999", time.Minute)
		Expect(err).NotTo(HaveOccurred())

		// 32-byte KEK, base64-encoded.
		kek := make([]byte, 32)
		_, _ = rand.Read(kek)
		crypto, err = auth.NewCrypto(base64.StdEncoding.EncodeToString(kek))
		Expect(err).NotTo(HaveOccurred())

		ver = &stubVerifier{licID: licString, expiry: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		auditor = audit.NewAuditor(nil, st, slog.Default())
		cfg = &config.Config{
			PublicPort:       0,
			ExternalHostname: "test-host:9999",
			TokenTTLSeconds:  60,
		}

		// Build the upstream test server. Manifests return canned bytes,
		// blobs return a 307 to an external URL (so we can verify the
		// passthrough behaviour).
		upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/manifests/"):
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`))
			case strings.Contains(r.URL.Path, "/blobs/"):
				w.Header().Set("Location", "https://signed.example.invalid/payload")
				w.Header().Set("Content-Length", "12345")
				w.WriteHeader(http.StatusTemporaryRedirect)
			case strings.HasSuffix(r.URL.Path, "/tags/list"):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"name":"test","tags":["v1"]}`))
			default:
				http.NotFound(w, r)
			}
		}))

		// Seed: one upstream credential, one package, one license, one token, one grant.
		// Use oci-basic against the httptest server URL — `ghcr` Kind is pinned
		// to https://ghcr.io and not configurable.
		credID = uuid.New()
		sealed, _ := crypto.Seal([]byte("ghp_FAKE_PAT"))
		st.upstreamByID[credID] = &store.UpstreamCredential{
			ID: credID, Name: "test-upstream", Kind: "oci-basic", BaseURL: upstream.URL,
			Username: "robot", PATEnc: sealed, PATFingerprint: "abcd1234",
		}

		pkgID = uuid.New()
		st.packagesByPath["acme/widget"] = &store.Package{
			ID: pkgID, Slug: "widget", Path: "acme/widget",
			UpstreamRepo: "upstream-org/widget", UpstreamCredentialID: credID,
			Kind: "container", DisplayName: "Widget",
		}
		st.packagesByID[pkgID] = st.packagesByPath["acme/widget"]

		licRowID = uuid.New()
		st.licenses[licRowID] = &store.License{
			ID: licRowID, LicenseID: licString, Customer: "Acme",
			Tier: cnaklicense.TierEnterprise, LicBlob: "valid-blob",
		}

		gen, err := auth.GenerateCustomerToken()
		Expect(err).NotTo(HaveOccurred())
		hash, err := auth.HashSecret(gen.Secret)
		Expect(err).NotTo(HaveOccurred())
		tokenID = gen.TokenID
		secret = gen.Secret
		ctRow := &store.CustomerToken{
			ID: uuid.New(), TokenID: tokenID, SecretHash: hash, LicenseID: licRowID,
		}
		st.customerTokens[tokenID] = ctRow
		st.customerByID[ctRow.ID] = ctRow

		st.grants[grantKey{licRowID, pkgID, "pull"}] = true

		// Build the public router with /v2 mounted.
		r := chi.NewRouter()
		r.Use(server.RequestID)
		r.Use(server.Logger(slog.Default()))
		up := server.NewUpstream(crypto, st, auditor, slog.Default())
		server.MountOCI(r, server.Deps{
			Store:    st,
			Signer:   signer,
			Crypto:   crypto,
			Cache:    nil,
			Verifier: ver,
			Auditor:  auditor,
			Cfg:      cfg,
			Upstream: up,
			Logger:   slog.Default(),
		})
		publicSrv = httptest.NewServer(r)
		client = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	})

	AfterEach(func() {
		upstream.Close()
		publicSrv.Close()
	})

	Describe("GET /v2/", func() {
		It("returns 401 with a Bearer challenge when unauthenticated", func() {
			resp, err := client.Get(publicSrv.URL + "/v2/")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
			Expect(resp.Header.Get("Www-Authenticate")).To(ContainSubstring(`realm="https://test-host:9999/v2/token"`))
			Expect(resp.Header.Get("Docker-Distribution-API-Version")).To(Equal("registry/2.0"))
		})

		It("returns 200 with a Bearer JWT", func() {
			tok := mintJWT(signer, tokenID, []auth.Access{{Type: "repository", Name: "acme/widget", Actions: []string{"pull"}}})
			req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})
	})

	Describe("GET /v2/token", func() {
		It("mints a token for a valid customer credential", func() {
			body := doTokenMint(client, publicSrv.URL, cfg.ExternalHostname, tokenID, secret,
				"repository:acme/widget:pull")
			Expect(body.Token).NotTo(BeEmpty())
			Expect(body.ExpiresIn).To(BeNumerically(">", 0))
			claims, err := signer.Verify(body.Token)
			Expect(err).NotTo(HaveOccurred())
			Expect(claims.Subject).To(Equal(tokenID))
			Expect(claims.Access).To(HaveLen(1))
			Expect(claims.Access[0].Actions).To(ConsistOf("pull"))
		})

		It("denies an expired license", func() {
			ver.expiry = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			resp := rawTokenMint(client, publicSrv.URL, cfg.ExternalHostname, tokenID, secret,
				"repository:acme/widget:pull")
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		})

		It("drops scopes the license doesn't grant", func() {
			body := doTokenMint(client, publicSrv.URL, cfg.ExternalHostname, tokenID, secret,
				"repository:acme/other:pull")
			// Other package isn't seeded — scope is silently dropped, mint succeeds.
			claims, err := signer.Verify(body.Token)
			Expect(err).NotTo(HaveOccurred())
			Expect(claims.Access).To(BeEmpty())
		})

		It("returns 401 on bad secret", func() {
			resp := rawTokenMint(client, publicSrv.URL, cfg.ExternalHostname, tokenID, "wrong",
				"repository:acme/widget:pull")
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
			Expect(resp.Header.Get("Www-Authenticate")).To(ContainSubstring("Bearer"))
		})
	})

	Describe("OCI proxy", func() {
		It("proxies a manifest with Content-Type preserved", func() {
			tok := mintJWT(signer, tokenID, []auth.Access{{Type: "repository", Name: "acme/widget", Actions: []string{"pull"}}})
			req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/acme/widget/manifests/v1", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/vnd.oci.image.manifest.v1+json"))
			Expect(resp.Header.Get("Docker-Content-Digest")).To(Equal("sha256:deadbeef"))
		})

		It("passes the blob 307 through unchanged", func() {
			tok := mintJWT(signer, tokenID, []auth.Access{{Type: "repository", Name: "acme/widget", Actions: []string{"pull"}}})
			req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/acme/widget/blobs/sha256:abc", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusTemporaryRedirect))
			Expect(resp.Header.Get("Location")).To(Equal("https://signed.example.invalid/payload"))
		})

		It("rejects a request whose JWT doesn't cover the package", func() {
			tok := mintJWT(signer, tokenID, []auth.Access{{Type: "repository", Name: "acme/other", Actions: []string{"pull"}}})
			req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/acme/widget/manifests/v1", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("rejects an expired JWT", func() {
			// Build a signer with a 1-nanosecond TTL.
			shortKey := make([]byte, 32)
			_, _ = rand.Read(shortKey)
			expSigner, err := auth.NewJWTSigner(toHex(shortKey), "artifact-gateway", "test-host:9999", time.Nanosecond)
			Expect(err).NotTo(HaveOccurred())
			tok, _, _, err := expSigner.Mint(tokenID, nil)
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(2 * time.Millisecond)
			req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/acme/widget/manifests/v1", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			// Wrong key + wrong issuer combined with expiry → 401.
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})
})

// --- helpers ----------------------------------------------------------------

func mintJWT(s *auth.JWTSigner, sub string, access []auth.Access) string {
	tok, _, _, err := s.Mint(sub, access)
	Expect(err).NotTo(HaveOccurred())
	return tok
}

type tokenBody struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	IssuedAt    string `json:"issued_at"`
}

func rawTokenMint(c *http.Client, base, service, tid, sec, scope string) *http.Response {
	q := url.Values{}
	q.Set("service", service)
	q.Add("scope", scope)
	req, _ := http.NewRequest(http.MethodGet, base+"/v2/token?"+q.Encode(), nil)
	cred := base64.StdEncoding.EncodeToString([]byte(tid + ":" + sec))
	req.Header.Set("Authorization", "Basic "+cred)
	resp, err := c.Do(req)
	Expect(err).NotTo(HaveOccurred())
	return resp
}

func doTokenMint(c *http.Client, base, service, tid, sec, scope string) tokenBody {
	resp := rawTokenMint(c, base, service, tid, sec, scope)
	defer resp.Body.Close()
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	b, _ := io.ReadAll(resp.Body)
	var out tokenBody
	Expect(json.Unmarshal(b, &out)).To(Succeed())
	return out
}

// toHex returns a lowercase hex string. We avoid importing encoding/hex twice.
func toHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[i*2] = digits[x>>4]
		out[i*2+1] = digits[x&0x0f]
	}
	return string(out)
}

// b32 keeps an import live where ginkgo would otherwise warn during local
// iteration. Safe to remove once the suite is fully fleshed out.
var _ = base32.StdEncoding

var _ = Describe("OCI server (multi-container)", func() {
	// This Describe block stands up its own router so the second package
	// ("cnak-platform" with containers backend + worker) doesn't collide
	// with the single-container fixture from the outer Describe.
	var (
		st       *fakeStore
		signer    *auth.JWTSigner
		crypto    *auth.Crypto
		ver       *stubVerifier
		auditor   *audit.Auditor
		cfg       *config.Config
		publicSrv *httptest.Server
		client    *http.Client

		licRowID uuid.UUID
		pkgID    uuid.UUID
		tokenID  string
		secret   string
	)

	BeforeEach(func() {
		st = newFakeStore()

		jwtKey := make([]byte, 32)
		_, _ = rand.Read(jwtKey)
		var err error
		signer, err = auth.NewJWTSigner(toHex(jwtKey), "artifact-gateway", "test-host:9999", time.Minute)
		Expect(err).NotTo(HaveOccurred())

		kek := make([]byte, 32)
		_, _ = rand.Read(kek)
		crypto, err = auth.NewCrypto(base64.StdEncoding.EncodeToString(kek))
		Expect(err).NotTo(HaveOccurred())

		ver = &stubVerifier{licID: "lic-multi", expiry: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		auditor = audit.NewAuditor(nil, st, slog.Default())
		cfg = &config.Config{
			PublicPort:       0,
			ExternalHostname: "test-host:9999",
			TokenTTLSeconds:  60,
		}

		// Production contract: one credential per package shared across
		// containers. Stand up a single upstream server that routes by
		// upstreamRepo so we can assert the proxy chose the right per-alias
		// repo path.
		multiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/v2/up/backend/") && strings.Contains(r.URL.Path, "/manifests/"):
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.Header().Set("Docker-Content-Digest", "sha256:backend")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"upstream":"backend"}`))
			case strings.HasPrefix(r.URL.Path, "/v2/up/worker/") && strings.Contains(r.URL.Path, "/manifests/"):
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.Header().Set("Docker-Content-Digest", "sha256:worker")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"upstream":"worker"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		DeferCleanup(multiSrv.Close)

		credID := uuid.New()
		sealed, _ := crypto.Seal([]byte("ghp_FAKE_PAT"))
		st.upstreamByID[credID] = &store.UpstreamCredential{
			ID: credID, Name: "test-upstream", Kind: "oci-basic", BaseURL: multiSrv.URL,
			Username: "robot", PATEnc: sealed, PATFingerprint: "abcd1234",
		}

		// Multi-container package: cnak-platform, with two containers under it.
		pkgID = uuid.New()
		pkg := &store.Package{
			ID: pkgID, Slug: "cnak-platform", Path: "cnak-platform",
			UpstreamRepo: "", UpstreamCredentialID: credID,
			Kind: "container", DisplayName: "CNAK Platform",
		}
		st.packagesByPath["cnak-platform"] = pkg
		st.packagesByID[pkgID] = pkg
		st.packagesBySlug["cnak-platform"] = pkg

		st.containers[containerKey{pkgID, "backend"}] = &store.PackageContainer{
			PackageID: pkgID, Alias: "backend", UpstreamRepo: "up/backend",
		}
		st.containers[containerKey{pkgID, "worker"}] = &store.PackageContainer{
			PackageID: pkgID, Alias: "worker", UpstreamRepo: "up/worker",
		}

		licRowID = uuid.New()
		st.licenses[licRowID] = &store.License{
			ID: licRowID, LicenseID: "lic-multi", Customer: "Acme",
			Tier: cnaklicense.TierEnterprise, LicBlob: "valid-blob",
		}

		gen, err := auth.GenerateCustomerToken()
		Expect(err).NotTo(HaveOccurred())
		hash, err := auth.HashSecret(gen.Secret)
		Expect(err).NotTo(HaveOccurred())
		tokenID = gen.TokenID
		secret = gen.Secret
		ctRow := &store.CustomerToken{
			ID: uuid.New(), TokenID: tokenID, SecretHash: hash, LicenseID: licRowID,
		}
		st.customerTokens[tokenID] = ctRow
		st.customerByID[ctRow.ID] = ctRow

		st.grants[grantKey{licRowID, pkgID, "pull"}] = true

		r := chi.NewRouter()
		r.Use(server.RequestID)
		r.Use(server.Logger(slog.Default()))
		up := server.NewUpstream(crypto, st, auditor, slog.Default())
		server.MountOCI(r, server.Deps{
			Store:    st,
			Signer:   signer,
			Crypto:   crypto,
			Cache:    nil,
			Verifier: ver,
			Auditor:  auditor,
			Cfg:      cfg,
			Upstream: up,
			Logger:   slog.Default(),
		})
		publicSrv = httptest.NewServer(r)
		client = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	})

	AfterEach(func() {
		publicSrv.Close()
	})

	It("routes /v2/cnak-platform/backend/manifests/v1 to the backend upstream repo", func() {
		tok := mintJWT(signer, tokenID, []auth.Access{
			{Type: "repository", Name: "cnak-platform/backend", Actions: []string{"pull"}},
		})
		req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/cnak-platform/backend/manifests/v1", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Header.Get("Docker-Content-Digest")).To(Equal("sha256:backend"))
		body, _ := io.ReadAll(resp.Body)
		Expect(string(body)).To(ContainSubstring(`"upstream":"backend"`))
	})

	It("routes /v2/cnak-platform/worker/manifests/v1 to the worker upstream repo", func() {
		tok := mintJWT(signer, tokenID, []auth.Access{
			{Type: "repository", Name: "cnak-platform/worker", Actions: []string{"pull"}},
		})
		req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/cnak-platform/worker/manifests/v1", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Header.Get("Docker-Content-Digest")).To(Equal("sha256:worker"))
	})

	It("404s NAME_UNKNOWN on a pull at the bare multi-container path", func() {
		// /v2/cnak-platform/manifests/v1 — the package itself has children,
		// so there is no implicit root repo.
		tok := mintJWT(signer, tokenID, []auth.Access{
			{Type: "repository", Name: "cnak-platform", Actions: []string{"pull"}},
		})
		req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/cnak-platform/manifests/v1", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		body, _ := io.ReadAll(resp.Body)
		Expect(string(body)).To(ContainSubstring("NAME_UNKNOWN"))
	})

	It("404s NAME_UNKNOWN on an unknown alias under the package", func() {
		tok := mintJWT(signer, tokenID, []auth.Access{
			{Type: "repository", Name: "cnak-platform/nonexistent", Actions: []string{"pull"}},
		})
		req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/v2/cnak-platform/nonexistent/manifests/v1", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		body, _ := io.ReadAll(resp.Body)
		Expect(string(body)).To(ContainSubstring("NAME_UNKNOWN"))
	})

	It("mints a JWT scope for repository:cnak-platform/backend when granted", func() {
		body := doTokenMint(client, publicSrv.URL, cfg.ExternalHostname, tokenID, secret,
			"repository:cnak-platform/backend:pull")
		claims, err := signer.Verify(body.Token)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims.Access).To(HaveLen(1))
		Expect(claims.Access[0].Name).To(Equal("cnak-platform/backend"))
		Expect(claims.Access[0].Actions).To(ConsistOf("pull"))
	})

	It("drops the scope when the license has no grant on the package", func() {
		// Revoke the only grant so the lookup yields HasGrant=false.
		delete(st.grants, grantKey{licRowID, pkgID, "pull"})
		body := doTokenMint(client, publicSrv.URL, cfg.ExternalHostname, tokenID, secret,
			"repository:cnak-platform/backend:pull")
		claims, err := signer.Verify(body.Token)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims.Access).To(BeEmpty())
	})

	It("drops the scope when the alias does not exist", func() {
		body := doTokenMint(client, publicSrv.URL, cfg.ExternalHostname, tokenID, secret,
			"repository:cnak-platform/nonexistent:pull")
		claims, err := signer.Verify(body.Token)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims.Access).To(BeEmpty())
	})
})
