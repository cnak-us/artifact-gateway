package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"github.com/cnak-us/artifact-gateway/server"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GitHubReleasesClient", func() {
	var (
		gh        *server.GitHubReleasesClient
		upstream  *httptest.Server
		signedURL string
		handler   func(w http.ResponseWriter, r *http.Request)
	)

	BeforeEach(func() {
		signedURL = "https://signed.example.invalid/asset-bytes?sig=abc"
		// Outer handler dispatches to the per-test `handler` set below so
		// each It() can swap behaviour without rebuilding the server.
		upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler(w, r)
		}))
		gh = &server.GitHubReleasesClient{
			Client:  &http.Client{Timeout: 5 * time.Second},
			APIBase: upstream.URL,
		}
	})

	AfterEach(func() {
		upstream.Close()
	})

	Describe("ListReleases", func() {
		It("returns non-draft releases parsed from the response body", func() {
			handler = func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/repos/acme/widget/releases"))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer pat-123"))
				Expect(r.Header.Get("Accept")).To(Equal("application/vnd.github+json"))
				w.Header().Set("X-RateLimit-Remaining", "4999")
				_, _ = w.Write([]byte(`[
                    {"tag_name":"v1.0.0","name":"v1","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z","html_url":"https://x/v1","assets":[{"id":1,"name":"a","size":10,"content_type":"application/gzip"}]},
                    {"tag_name":"v0.9.0","name":"v0.9","draft":true,"prerelease":false,"published_at":"2025-12-01T00:00:00Z","html_url":"https://x/v09","assets":[]}
                ]`))
			}
			rels, err := gh.ListReleases(context.Background(), "acme/widget", "pat-123", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(rels).To(HaveLen(1))
			Expect(rels[0].TagName).To(Equal("v1.0.0"))
			Expect(rels[0].Assets).To(HaveLen(1))
			Expect(rels[0].Assets[0].ID).To(Equal(int64(1)))
		})
	})

	Describe("GetRelease", func() {
		It("hits /releases/latest when tag is 'latest'", func() {
			handler = func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/repos/acme/widget/releases/latest"))
				_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","name":"v1.2.3","assets":[]}`))
			}
			rel, err := gh.GetRelease(context.Background(), "acme/widget", "latest", "pat")
			Expect(err).NotTo(HaveOccurred())
			Expect(rel.TagName).To(Equal("v1.2.3"))
		})

		It("hits /releases/tags/<tag> for a literal tag", func() {
			handler = func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/repos/acme/widget/releases/tags/v1.4.2"))
				_, _ = w.Write([]byte(`{"tag_name":"v1.4.2","assets":[{"id":42,"name":"file.tgz","size":100,"content_type":"application/gzip"}]}`))
			}
			rel, err := gh.GetRelease(context.Background(), "acme/widget", "v1.4.2", "pat")
			Expect(err).NotTo(HaveOccurred())
			Expect(rel.TagName).To(Equal("v1.4.2"))
			Expect(rel.Assets[0].ID).To(Equal(int64(42)))
		})
	})

	Describe("AssetDownloadURL", func() {
		It("returns the redirect Location for a 302 response", func() {
			handler = func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/repos/acme/widget/releases/assets/42"))
				Expect(r.Header.Get("Accept")).To(Equal("application/octet-stream"))
				w.Header().Set("Location", signedURL)
				w.WriteHeader(http.StatusFound)
			}
			loc, status, err := gh.AssetDownloadURL(context.Background(), "acme/widget", 42, "pat")
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(http.StatusFound))
			Expect(loc).To(Equal(signedURL))
		})

		It("returns the upstream status on a 404", func() {
			handler = func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			}
			_, status, err := gh.AssetDownloadURL(context.Background(), "acme/widget", 999, "pat")
			Expect(err).To(HaveOccurred())
			Expect(status).To(Equal(http.StatusNotFound))
		})
	})

	Describe("rate limiting", func() {
		It("returns *RateLimitError on a 403 with X-RateLimit-Remaining: 0", func() {
			reset := time.Now().Add(30 * time.Second).Unix()
			handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			}
			_, err := gh.ListReleases(context.Background(), "acme/widget", "pat", 5)
			Expect(err).To(HaveOccurred())
			Expect(server.IsRateLimit(err)).To(BeTrue(), "expected RateLimitError, got %T %v", err, err)
		})
	})
})
