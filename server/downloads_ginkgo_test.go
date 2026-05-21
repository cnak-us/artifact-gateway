package server_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	agoidc "github.com/cnak-us/artifact-gateway/oidc"
	"github.com/cnak-us/artifact-gateway/server"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Downloads", func() {
	var (
		st          *fakeStore
		signer      *auth.JWTSigner
		crypto      *auth.Crypto
		auditor     *audit.Auditor
		cfg         *config.Config
		sessions    *agoidc.Manager
		ghUpstream  *httptest.Server
		publicSrv   *httptest.Server
		client      *http.Client
		signedAsset string

		tokenID  string
		secret   string
		licRowID uuid.UUID
		pkgID    uuid.UUID
		credID   uuid.UUID
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

		sessionKey := make([]byte, 32)
		_, _ = rand.Read(sessionKey)
		sessions, err = agoidc.NewManager(toHex(sessionKey), "ag_customer_session", false)
		Expect(err).NotTo(HaveOccurred())

		auditor = audit.NewAuditor(nil, st, slog.Default())
		cfg = &config.Config{ExternalHostname: "test-host:9999", PublicPort: 0}

		signedAsset = "https://signed.example.invalid/cnak-linux-amd64.tar.gz?sig=abc"

		// Fake GitHub API: /repos/.../releases/latest, /releases/tags/{tag}, /releases/assets/{id}.
		ghUpstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/releases/latest"):
				_, _ = w.Write([]byte(`{
					"tag_name":"v1.0.0","name":"v1","prerelease":false,"published_at":"2026-05-14T00:00:00Z",
					"assets":[{"id":42,"name":"cnak-linux-amd64.tar.gz","size":47185920,"content_type":"application/gzip"}]
				}`))
			case strings.HasSuffix(r.URL.Path, "/releases/tags/v1.0.0"):
				_, _ = w.Write([]byte(`{
					"tag_name":"v1.0.0","name":"v1","prerelease":false,"published_at":"2026-05-14T00:00:00Z",
					"assets":[{"id":42,"name":"cnak-linux-amd64.tar.gz","size":47185920,"content_type":"application/gzip"}]
				}`))
			case strings.HasSuffix(r.URL.Path, "/releases/assets/42"):
				w.Header().Set("Location", signedAsset)
				w.WriteHeader(http.StatusFound)
			default:
				http.NotFound(w, r)
			}
		}))

		// Seed: credential, github-release package, license, customer token, grant.
		credID = uuid.New()
		sealed, _ := crypto.Seal([]byte("ghp_FAKE_PAT"))
		st.upstreamByID[credID] = &store.UpstreamCredential{
			ID: credID, Name: "github-api", Kind: "github-api",
			Username: "robot", PATEnc: sealed, PATFingerprint: "abcd1234",
		}

		pkgID = uuid.New()
		pkg := &store.Package{
			ID: pkgID, Slug: "cnak", Path: "cnak-us/cnak",
			UpstreamRepo: "", UpstreamCredentialID: credID,
			Kind: "binary", DisplayName: "CNAK",
			Source: "github-release", GitHubRepo: "cnak-us/cnak",
			ReleasePattern: "latest",
		}
		st.packagesByID[pkgID] = pkg
		st.packagesBySlug["cnak"] = pkg

		licRowID = uuid.New()
		st.licenses[licRowID] = &store.License{
			ID: licRowID, LicenseID: "lic-cnak", Customer: "Acme",
			LicBlob: "valid-blob",
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

		gh := &server.GitHubReleasesClient{
			Client:  &http.Client{Timeout: 5 * time.Second},
			APIBase: ghUpstream.URL,
		}
		r := chi.NewRouter()
		r.Use(server.RequestID)
		server.MountDownloads(r, server.DownloadsDeps{
			Store:    st,
			Crypto:   crypto,
			Signer:   signer,
			GH:       gh,
			Cfg:      cfg,
			Auditor:  auditor,
			Sessions: sessions,
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
		ghUpstream.Close()
		publicSrv.Close()
	})

	Describe("GET /download/{slug}", func() {
		It("lists releases for a Basic-authed CLI client", func() {
			req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/download/cnak", nil)
			cred := base64.StdEncoding.EncodeToString([]byte(tokenID + ":" + secret))
			req.Header.Set("Authorization", "Basic "+cred)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var body struct {
				Releases []struct {
					Tag    string `json:"tag"`
					Assets []struct {
						Name        string `json:"name"`
						DownloadURL string `json:"download_url"`
					} `json:"assets"`
				} `json:"releases"`
			}
			Expect(json.NewDecoder(resp.Body).Decode(&body)).To(Succeed())
			Expect(body.Releases).To(HaveLen(1))
			Expect(body.Releases[0].Tag).To(Equal("v1.0.0"))
			Expect(body.Releases[0].Assets).To(HaveLen(1))
			Expect(body.Releases[0].Assets[0].DownloadURL).
				To(Equal("/download/cnak/v1.0.0/cnak-linux-amd64.tar.gz"))
		})

		It("rejects requests without credentials", func() {
			resp, err := client.Get(publicSrv.URL + "/download/cnak")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("returns 403 when the license has no grant", func() {
			delete(st.grants, grantKey{licRowID, pkgID, "pull"})
			req, _ := http.NewRequest(http.MethodGet, publicSrv.URL+"/download/cnak", nil)
			cred := base64.StdEncoding.EncodeToString([]byte(tokenID + ":" + secret))
			req.Header.Set("Authorization", "Basic "+cred)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		})
	})

	Describe("GET /download/{slug}/{tag}/{asset}", func() {
		It("302s to the GitHub-signed CDN URL with Content-Disposition", func() {
			req, _ := http.NewRequest(http.MethodGet,
				publicSrv.URL+"/download/cnak/v1.0.0/cnak-linux-amd64.tar.gz", nil)
			cred := base64.StdEncoding.EncodeToString([]byte(tokenID + ":" + secret))
			req.Header.Set("Authorization", "Basic "+cred)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusFound))
			Expect(resp.Header.Get("Location")).To(Equal(signedAsset))
			Expect(resp.Header.Get("Content-Disposition")).
				To(ContainSubstring(`filename="cnak-linux-amd64.tar.gz"`))
		})

		It("returns 404 when the asset name is unknown", func() {
			req, _ := http.NewRequest(http.MethodGet,
				publicSrv.URL+"/download/cnak/v1.0.0/missing.tar.gz", nil)
			cred := base64.StdEncoding.EncodeToString([]byte(tokenID + ":" + secret))
			req.Header.Set("Authorization", "Basic "+cred)
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})
	})

	Describe("POST /catalog/api/downloads/sign + /download/_signed/{token}", func() {
		It("issues a signed URL that redirects without re-auth", func() {
			// First seed a customer cookie by writing one directly via the
			// Manager — we don't have a /catalog/login route mounted in this
			// test, so synthesize the session.
			ct := st.customerTokens[tokenID]
			cookieRec := httptest.NewRecorder()
			Expect(sessions.Issue(cookieRec, agoidc.Session{
				UserID: ct.ID,
				Email:  tokenID,
				Role:   "customer",
			})).To(Succeed())
			sessionCookie := cookieRec.Result().Cookies()[0]

			body, _ := json.Marshal(map[string]string{
				"slug":  "cnak",
				"tag":   "v1.0.0",
				"asset": "cnak-linux-amd64.tar.gz",
			})
			req, _ := http.NewRequest(http.MethodPost,
				publicSrv.URL+"/catalog/api/downloads/sign", bytes.NewReader(body))
			req.AddCookie(sessionCookie)
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var signResp struct {
				URL       string `json:"url"`
				ExpiresIn int    `json:"expires_in"`
			}
			Expect(json.NewDecoder(resp.Body).Decode(&signResp)).To(Succeed())
			Expect(signResp.URL).To(HavePrefix("/download/_signed/"))
			Expect(signResp.ExpiresIn).To(BeNumerically(">", 0))

			// Now hit the signed URL without any auth header. Should 302 to
			// the GitHub-signed CDN URL.
			req2, _ := http.NewRequest(http.MethodGet, publicSrv.URL+signResp.URL, nil)
			resp2, err := client.Do(req2)
			Expect(err).NotTo(HaveOccurred())
			defer resp2.Body.Close()
			Expect(resp2.StatusCode).To(Equal(http.StatusFound))
			Expect(resp2.Header.Get("Location")).To(Equal(signedAsset))
		})

		It("rejects an invalid signed token", func() {
			resp, err := client.Get(publicSrv.URL + "/download/_signed/not-a-real-jwt")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})
})

// avoid an unused-import error if a future edit removes `io`.
var _ = io.Discard
