package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// packagesFakeStore is a focused fake covering only the methods the
// createPackage handler actually touches. Kept distinct from the other
// fakes so the assertions can inspect the exact row written.
type packagesFakeStore struct {
	mu sync.Mutex

	inserts   []store.Package
	insertErr error
}

func (s *packagesFakeStore) InsertPackage(_ context.Context, p *store.Package) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertErr != nil {
		return s.insertErr
	}
	p.CreatedAt = time.Now()
	p.UpdatedAt = time.Now()
	s.inserts = append(s.inserts, *p)
	return nil
}

// Audit sink — no-op so audit.Auditor doesn't blow up on LogResourceMutation.
func (*packagesFakeStore) InsertAuditEvent(audit.AuditEvent) error { return nil }

// Everything else panics — createPackage must not reach these.
func (*packagesFakeStore) GetUserByEmail(context.Context, string) (*store.User, error) {
	panic("unused")
}
func (*packagesFakeStore) GetUserByOIDC(context.Context, uuid.UUID, string) (*store.User, error) {
	panic("unused")
}
func (*packagesFakeStore) InsertUser(context.Context, *store.User) error   { panic("unused") }
func (*packagesFakeStore) UpdateUser(context.Context, *store.User) error   { panic("unused") }
func (*packagesFakeStore) ListUsers(context.Context) ([]store.User, error) { panic("unused") }
func (*packagesFakeStore) CountUsers(context.Context) (int, error)         { panic("unused") }
func (*packagesFakeStore) ListOIDCProviders(context.Context) ([]store.OIDCProvider, error) {
	panic("unused")
}
func (*packagesFakeStore) GetOIDCProvider(context.Context, uuid.UUID) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*packagesFakeStore) GetOIDCProviderByName(context.Context, string) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*packagesFakeStore) InsertOIDCProvider(context.Context, *store.OIDCProvider) error {
	panic("unused")
}
func (*packagesFakeStore) DeleteOIDCProvider(context.Context, uuid.UUID) error { panic("unused") }
func (*packagesFakeStore) ListUpstreamCredentials(context.Context) ([]store.UpstreamCredential, error) {
	panic("unused")
}
func (*packagesFakeStore) GetUpstreamCredential(context.Context, uuid.UUID) (*store.UpstreamCredential, error) {
	panic("unused")
}
func (*packagesFakeStore) InsertUpstreamCredential(context.Context, *store.UpstreamCredential) error {
	panic("unused")
}
func (*packagesFakeStore) DeleteUpstreamCredential(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*packagesFakeStore) TouchUpstreamCredential(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*packagesFakeStore) ListPackages(context.Context) ([]store.Package, error) {
	panic("unused")
}
func (*packagesFakeStore) GetPackage(context.Context, uuid.UUID) (*store.Package, error) {
	panic("unused")
}
func (*packagesFakeStore) GetPackageByPath(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*packagesFakeStore) GetPackageBySlug(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*packagesFakeStore) UpdatePackage(context.Context, *store.Package) error { panic("unused") }
func (*packagesFakeStore) DeletePackage(context.Context, uuid.UUID) error      { panic("unused") }
func (*packagesFakeStore) ListLicenses(context.Context) ([]store.License, error) {
	panic("unused")
}
func (*packagesFakeStore) GetLicense(context.Context, uuid.UUID) (*store.License, error) {
	panic("unused")
}
func (*packagesFakeStore) GetLicenseByLicenseID(context.Context, string) (*store.License, error) {
	panic("unused")
}
func (*packagesFakeStore) InsertLicense(context.Context, *store.License) error {
	panic("unused")
}
func (*packagesFakeStore) RevokeLicense(context.Context, uuid.UUID) error { panic("unused") }
func (*packagesFakeStore) DeleteLicense(context.Context, uuid.UUID) error { panic("unused") }
func (*packagesFakeStore) SetLicenseCustomerRotate(context.Context, uuid.UUID, bool) error {
	panic("unused")
}
func (*packagesFakeStore) ListCustomerTokens(context.Context, *uuid.UUID) ([]store.CustomerToken, error) {
	panic("unused")
}
func (*packagesFakeStore) GetCustomerToken(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	panic("unused")
}
func (*packagesFakeStore) GetCustomerTokenByTokenID(context.Context, string) (*store.CustomerToken, error) {
	panic("unused")
}
func (*packagesFakeStore) InsertCustomerToken(context.Context, *store.CustomerToken) error {
	panic("unused")
}
func (*packagesFakeStore) RevokeCustomerToken(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*packagesFakeStore) TouchCustomerToken(context.Context, uuid.UUID) error { panic("unused") }
func (*packagesFakeStore) CountActiveCustomerTokens(context.Context) (int, error) {
	panic("unused")
}
func (*packagesFakeStore) ListActiveCustomerTokenForLicense(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	panic("unused")
}
func (*packagesFakeStore) RotateCustomerTokenForLicense(context.Context, uuid.UUID, *uuid.UUID, string, string, string) (uuid.UUID, error) {
	panic("unused")
}
func (*packagesFakeStore) ListGrantsForLicense(context.Context, uuid.UUID) ([]store.PackageGrant, error) {
	panic("unused")
}
func (*packagesFakeStore) GrantedPackagesForLicense(context.Context, uuid.UUID) ([]store.Package, error) {
	panic("unused")
}
func (*packagesFakeStore) ReplaceGrantsForLicense(context.Context, uuid.UUID, []uuid.UUID, []string) error {
	panic("unused")
}
func (*packagesFakeStore) HasGrant(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	panic("unused")
}
func (*packagesFakeStore) ListAuditEvents(context.Context, int, *time.Time) ([]audit.AuditEvent, error) {
	panic("unused")
}
func (*packagesFakeStore) ListStaticAdmins(context.Context) ([]store.StaticAdmin, error) {
	panic("unused")
}
func (*packagesFakeStore) GetStaticAdminByEmail(context.Context, string) (*store.StaticAdmin, error) {
	return nil, store.ErrNotFound
}
func (*packagesFakeStore) UpsertStaticAdmin(context.Context, *store.StaticAdmin) error {
	panic("unused")
}
func (*packagesFakeStore) DeleteStaticAdmin(context.Context, uuid.UUID) error { panic("unused") }
func (*packagesFakeStore) ListRootKeys(context.Context) ([]store.RootKey, error) {
	panic("unused")
}
func (*packagesFakeStore) GetRootKey(context.Context, uuid.UUID) (*store.RootKey, error) {
	panic("unused")
}
func (*packagesFakeStore) GetRootKeyByFingerprint(context.Context, string) (*store.RootKey, error) {
	panic("unused")
}
func (*packagesFakeStore) GetActiveSigningKey(context.Context) (*store.RootKey, error) {
	panic("unused")
}
func (*packagesFakeStore) InsertRootKey(context.Context, *store.RootKey) error { panic("unused") }
func (*packagesFakeStore) SetActiveRootKey(context.Context, uuid.UUID) error   { panic("unused") }
func (*packagesFakeStore) DeleteRootKey(context.Context, uuid.UUID) error      { panic("unused") }
func (*packagesFakeStore) ListContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*packagesFakeStore) ListManifestContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*packagesFakeStore) AddContact(context.Context, *store.LicenseContact) error {
	panic("unused")
}
func (*packagesFakeStore) RemoveContact(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (*packagesFakeStore) ReplaceManifestContactsForLicense(context.Context, uuid.UUID, []store.LicenseContact) error {
	panic("unused")
}
func (*packagesFakeStore) FindLicensesByContactEmail(context.Context, string) ([]store.License, error) {
	panic("unused")
}
func (*packagesFakeStore) ListContainersForPackage(context.Context, uuid.UUID) ([]store.PackageContainer, error) {
	panic("unused")
}
func (*packagesFakeStore) ListManifestContainersForPackage(context.Context, uuid.UUID) ([]store.PackageContainer, error) {
	panic("unused")
}
func (*packagesFakeStore) GetContainer(context.Context, uuid.UUID, string) (*store.PackageContainer, error) {
	panic("unused")
}
func (*packagesFakeStore) UpsertContainer(context.Context, *store.PackageContainer) error {
	panic("unused")
}
func (*packagesFakeStore) DeleteContainer(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (*packagesFakeStore) ReplaceManifestContainersForPackage(context.Context, uuid.UUID, []store.PackageContainer) error {
	panic("unused")
}
func (*packagesFakeStore) GetBranding(context.Context) (*store.Branding, error) {
	panic("unused")
}
func (*packagesFakeStore) SetBranding(context.Context, *store.Branding) error { panic("unused") }
func (*packagesFakeStore) Close()                                             {}

var _ store.DataStore = (*packagesFakeStore)(nil)

var _ = Describe("POST /admin/packages", func() {
	var (
		st     *packagesFakeStore
		srv    *httptest.Server
		client *http.Client
		credID uuid.UUID
	)

	BeforeEach(func() {
		st = &packagesFakeStore{}
		auditor := audit.NewAuditor(nil, st, slog.Default())
		credID = uuid.New()

		r := chi.NewRouter()
		deps := AdminDeps{
			Store:   st,
			Auditor: auditor,
			Logger:  slog.Default(),
		}
		// Mount only the package-create handler, bypassing admin auth — the
		// handler doesn't depend on session context except for audit username,
		// and actorEmail tolerates a nil session.
		r.Route("/api/v1/packages", func(r chi.Router) {
			r.Post("/", createPackage(deps))
		})
		srv = httptest.NewServer(r)
		client = srv.Client()
	})

	AfterEach(func() { srv.Close() })

	postPackage := func(body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost,
			srv.URL+"/api/v1/packages/",
			bytes.NewBufferString(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	It("creates a package row and returns 201 with the assigned id", func() {
		body, _ := json.Marshal(map[string]any{
			"slug":                   "cnak-core",
			"path":                   "cnak-us/cnak-core",
			"kind":                   "container",
			"upstream_credential_id": credID.String(),
			"upstream_repo":          "ghcr.io/cnak-us/cnak-core",
			"display_name":           "CNAK Core",
		})
		resp := postPackage(string(body))
		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusCreated))

		Expect(st.inserts).To(HaveLen(1))
		row := st.inserts[0]
		Expect(row.ID).NotTo(Equal(uuid.Nil))
		Expect(row.Slug).To(Equal("cnak-core"))
		Expect(row.Path).To(Equal("cnak-us/cnak-core"))
		Expect(row.Kind).To(Equal("container"))
		Expect(row.UpstreamCredentialID).To(Equal(credID))
		Expect(row.UpstreamRepo).To(Equal("ghcr.io/cnak-us/cnak-core"))
		Expect(row.DisplayName).To(Equal("CNAK Core"))

		// Body should echo the created row.
		b, _ := io.ReadAll(resp.Body)
		var got packageDTO
		Expect(json.Unmarshal(b, &got)).To(Succeed())
		Expect(got.ID).To(Equal(row.ID))
		Expect(got.Slug).To(Equal("cnak-core"))
	})

	It("rejects a request missing slug/path/kind", func() {
		// Missing slug — handler bundles the three required fields into one error.
		resp := postPackage(`{"path":"ns/p","kind":"container","upstream_credential_id":"` + credID.String() + `"}`)
		defer func() { _ = resp.Body.Close() }()
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(st.inserts).To(BeEmpty())
	})

	It("rejects a request missing upstream_credential_id", func() {
		resp := postPackage(`{"slug":"cnak-core","path":"cnak-us/cnak-core","kind":"container"}`)
		defer func() { _ = resp.Body.Close() }()
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(st.inserts).To(BeEmpty())
	})

	It("rejects source=github-release without github_repo", func() {
		body, _ := json.Marshal(map[string]any{
			"slug":                   "cnak-cli",
			"path":                   "cnak-us/cnak-cli",
			"kind":                   "binary",
			"upstream_credential_id": credID.String(),
			"source":                 "github-release",
		})
		resp := postPackage(string(body))
		defer func() { _ = resp.Body.Close() }()
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(st.inserts).To(BeEmpty())
	})

	It("applies default release_pattern and asset_pattern for source=github-release", func() {
		body, _ := json.Marshal(map[string]any{
			"slug":                   "cnak-cli",
			"path":                   "cnak-us/cnak-cli",
			"kind":                   "binary",
			"upstream_credential_id": credID.String(),
			"source":                 "github-release",
			"github_repo":            "cnak-us/cnak-cli",
		})
		resp := postPackage(string(body))
		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		Expect(st.inserts).To(HaveLen(1))
		row := st.inserts[0]
		Expect(row.Source).To(Equal("github-release"))
		Expect(row.GitHubRepo).To(Equal("cnak-us/cnak-cli"))
		Expect(row.ReleasePattern).To(Equal("latest"))
		Expect(row.AssetPattern).To(Equal("*"))
	})
})
