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

// containersFakeStore is a focused fake covering only the methods the
// container admin handlers actually touch. Kept distinct from the other
// fakes so the assertions can inspect the exact rows written.
type containersFakeStore struct {
	mu sync.Mutex

	listResult []store.PackageContainer
	listErr    error

	upserts []store.PackageContainer
	deletes []deletedContainer

	upsertErr error
	deleteErr error
}

type deletedContainer struct {
	PackageID uuid.UUID
	Alias     string
}

func (s *containersFakeStore) ListContainersForPackage(_ context.Context, _ uuid.UUID) ([]store.PackageContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.PackageContainer(nil), s.listResult...), s.listErr
}

func (s *containersFakeStore) UpsertContainer(_ context.Context, c *store.PackageContainer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertErr != nil {
		return s.upsertErr
	}
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	s.upserts = append(s.upserts, *c)
	return nil
}

func (s *containersFakeStore) DeleteContainer(_ context.Context, pkg uuid.UUID, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletes = append(s.deletes, deletedContainer{pkg, alias})
	return nil
}

// Audit sink — no-op.
func (*containersFakeStore) InsertAuditEvent(audit.AuditEvent) error { return nil }

// Everything else panics — the container handlers must not reach these.
func (*containersFakeStore) GetUserByEmail(context.Context, string) (*store.User, error) {
	panic("unused")
}
func (*containersFakeStore) GetUserByOIDC(context.Context, uuid.UUID, string) (*store.User, error) {
	panic("unused")
}
func (*containersFakeStore) InsertUser(context.Context, *store.User) error   { panic("unused") }
func (*containersFakeStore) UpdateUser(context.Context, *store.User) error   { panic("unused") }
func (*containersFakeStore) ListUsers(context.Context) ([]store.User, error) { panic("unused") }
func (*containersFakeStore) CountUsers(context.Context) (int, error)         { panic("unused") }
func (*containersFakeStore) ListOIDCProviders(context.Context) ([]store.OIDCProvider, error) {
	panic("unused")
}
func (*containersFakeStore) GetOIDCProvider(context.Context, uuid.UUID) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*containersFakeStore) GetOIDCProviderByName(context.Context, string) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*containersFakeStore) InsertOIDCProvider(context.Context, *store.OIDCProvider) error {
	panic("unused")
}
func (*containersFakeStore) DeleteOIDCProvider(context.Context, uuid.UUID) error { panic("unused") }
func (*containersFakeStore) ListUpstreamCredentials(context.Context) ([]store.UpstreamCredential, error) {
	panic("unused")
}
func (*containersFakeStore) GetUpstreamCredential(context.Context, uuid.UUID) (*store.UpstreamCredential, error) {
	panic("unused")
}
func (*containersFakeStore) InsertUpstreamCredential(context.Context, *store.UpstreamCredential) error {
	panic("unused")
}
func (*containersFakeStore) DeleteUpstreamCredential(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*containersFakeStore) TouchUpstreamCredential(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*containersFakeStore) ListPackages(context.Context) ([]store.Package, error) {
	panic("unused")
}
func (*containersFakeStore) GetPackage(context.Context, uuid.UUID) (*store.Package, error) {
	panic("unused")
}
func (*containersFakeStore) GetPackageByPath(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*containersFakeStore) GetPackageBySlug(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*containersFakeStore) InsertPackage(context.Context, *store.Package) error { panic("unused") }
func (*containersFakeStore) UpdatePackage(context.Context, *store.Package) error { panic("unused") }
func (*containersFakeStore) DeletePackage(context.Context, uuid.UUID) error      { panic("unused") }
func (*containersFakeStore) ListLicenses(context.Context) ([]store.License, error) {
	panic("unused")
}
func (*containersFakeStore) GetLicense(context.Context, uuid.UUID) (*store.License, error) {
	panic("unused")
}
func (*containersFakeStore) GetLicenseByLicenseID(context.Context, string) (*store.License, error) {
	panic("unused")
}
func (*containersFakeStore) InsertLicense(context.Context, *store.License) error {
	panic("unused")
}
func (*containersFakeStore) RevokeLicense(context.Context, uuid.UUID) error { panic("unused") }
func (*containersFakeStore) DeleteLicense(context.Context, uuid.UUID) error { panic("unused") }
func (*containersFakeStore) SetLicenseCustomerRotate(context.Context, uuid.UUID, bool) error {
	panic("unused")
}
func (*containersFakeStore) ListCustomerTokens(context.Context, *uuid.UUID) ([]store.CustomerToken, error) {
	panic("unused")
}
func (*containersFakeStore) GetCustomerToken(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	panic("unused")
}
func (*containersFakeStore) GetCustomerTokenByTokenID(context.Context, string) (*store.CustomerToken, error) {
	panic("unused")
}
func (*containersFakeStore) InsertCustomerToken(context.Context, *store.CustomerToken) error {
	panic("unused")
}
func (*containersFakeStore) RevokeCustomerToken(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*containersFakeStore) TouchCustomerToken(context.Context, uuid.UUID) error { panic("unused") }
func (*containersFakeStore) CountActiveCustomerTokens(context.Context) (int, error) {
	panic("unused")
}
func (*containersFakeStore) ListActiveCustomerTokenForLicense(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	panic("unused")
}
func (*containersFakeStore) RotateCustomerTokenForLicense(context.Context, uuid.UUID, *uuid.UUID, string, string, string) (uuid.UUID, error) {
	panic("unused")
}
func (*containersFakeStore) ListGrantsForLicense(context.Context, uuid.UUID) ([]store.PackageGrant, error) {
	panic("unused")
}
func (*containersFakeStore) GrantedPackagesForLicense(context.Context, uuid.UUID) ([]store.Package, error) {
	panic("unused")
}
func (*containersFakeStore) ReplaceGrantsForLicense(context.Context, uuid.UUID, []uuid.UUID, []string) error {
	panic("unused")
}
func (*containersFakeStore) HasGrant(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	panic("unused")
}
func (*containersFakeStore) ListAuditEvents(context.Context, int, *time.Time) ([]audit.AuditEvent, error) {
	panic("unused")
}
func (*containersFakeStore) ListStaticAdmins(context.Context) ([]store.StaticAdmin, error) {
	panic("unused")
}
func (*containersFakeStore) GetStaticAdminByEmail(context.Context, string) (*store.StaticAdmin, error) {
	return nil, store.ErrNotFound
}
func (*containersFakeStore) UpsertStaticAdmin(context.Context, *store.StaticAdmin) error {
	panic("unused")
}
func (*containersFakeStore) DeleteStaticAdmin(context.Context, uuid.UUID) error { panic("unused") }
func (*containersFakeStore) ListRootKeys(context.Context) ([]store.RootKey, error) {
	panic("unused")
}
func (*containersFakeStore) GetRootKey(context.Context, uuid.UUID) (*store.RootKey, error) {
	panic("unused")
}
func (*containersFakeStore) GetRootKeyByFingerprint(context.Context, string) (*store.RootKey, error) {
	panic("unused")
}
func (*containersFakeStore) GetActiveSigningKey(context.Context) (*store.RootKey, error) {
	panic("unused")
}
func (*containersFakeStore) InsertRootKey(context.Context, *store.RootKey) error { panic("unused") }
func (*containersFakeStore) SetActiveRootKey(context.Context, uuid.UUID) error   { panic("unused") }
func (*containersFakeStore) DeleteRootKey(context.Context, uuid.UUID) error      { panic("unused") }
func (*containersFakeStore) ListContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*containersFakeStore) ListManifestContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*containersFakeStore) AddContact(context.Context, *store.LicenseContact) error {
	panic("unused")
}
func (*containersFakeStore) RemoveContact(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (*containersFakeStore) ReplaceManifestContactsForLicense(context.Context, uuid.UUID, []store.LicenseContact) error {
	panic("unused")
}
func (*containersFakeStore) FindLicensesByContactEmail(context.Context, string) ([]store.License, error) {
	panic("unused")
}
func (*containersFakeStore) ListManifestContainersForPackage(context.Context, uuid.UUID) ([]store.PackageContainer, error) {
	panic("unused")
}
func (*containersFakeStore) GetContainer(context.Context, uuid.UUID, string) (*store.PackageContainer, error) {
	panic("unused")
}
func (*containersFakeStore) ReplaceManifestContainersForPackage(context.Context, uuid.UUID, []store.PackageContainer) error {
	panic("unused")
}
func (*containersFakeStore) GetBranding(context.Context) (*store.Branding, error) {
	panic("unused")
}
func (*containersFakeStore) SetBranding(context.Context, *store.Branding) error { panic("unused") }
func (*containersFakeStore) Close()                                             {}

var _ store.DataStore = (*containersFakeStore)(nil)

var _ = Describe("Admin package container handlers", func() {
	var (
		st     *containersFakeStore
		srv    *httptest.Server
		client *http.Client
		pkgID  uuid.UUID
	)

	BeforeEach(func() {
		st = &containersFakeStore{}
		auditor := audit.NewAuditor(nil, st, slog.Default())
		pkgID = uuid.New()

		r := chi.NewRouter()
		deps := AdminDeps{
			Store:   st,
			Auditor: auditor,
			Logger:  slog.Default(),
		}
		// Mount only the container handlers, bypassing admin auth — the
		// handlers don't depend on session context except for audit username.
		r.Route("/api/v1/packages", func(r chi.Router) {
			r.Get("/{id}/containers", listPackageContainers(deps))
			r.Post("/{id}/containers", upsertPackageContainer(deps))
			r.Delete("/{id}/containers/{alias}", deletePackageContainer(deps))
		})
		srv = httptest.NewServer(r)
		client = srv.Client()
	})

	AfterEach(func() { srv.Close() })

	postContainer := func(body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost,
			srv.URL+"/api/v1/packages/"+pkgID.String()+"/containers",
			bytes.NewBufferString(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	listContainers := func() *http.Response {
		resp, err := client.Get(srv.URL + "/api/v1/packages/" + pkgID.String() + "/containers")
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	deleteContainer := func(alias string) *http.Response {
		req, err := http.NewRequest(http.MethodDelete,
			srv.URL+"/api/v1/packages/"+pkgID.String()+"/containers/"+alias, nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	Describe("POST /packages/{id}/containers", func() {
		It("creates a container row tagged source='' (UI-owned)", func() {
			resp := postContainer(`{"alias":"backend","upstream_repo":"ns/backend","display_name":"Backend"}`)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			Expect(st.upserts).To(HaveLen(1))
			row := st.upserts[0]
			Expect(row.PackageID).To(Equal(pkgID))
			Expect(row.Alias).To(Equal("backend"))
			Expect(row.UpstreamRepo).To(Equal("ns/backend"))
			Expect(row.DisplayName).To(Equal("Backend"))
			Expect(row.Source).To(Equal(""))
		})

		It("rejects an empty alias", func() {
			resp := postContainer(`{"alias":"","upstream_repo":"ns/x"}`)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			Expect(st.upserts).To(BeEmpty())
		})

		It("rejects an alias containing '/'", func() {
			resp := postContainer(`{"alias":"has/slash","upstream_repo":"ns/x"}`)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			Expect(st.upserts).To(BeEmpty())
		})

		It("rejects an empty upstream_repo", func() {
			resp := postContainer(`{"alias":"backend","upstream_repo":""}`)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			Expect(st.upserts).To(BeEmpty())
		})
	})

	Describe("GET /packages/{id}/containers", func() {
		It("returns the rows from the store", func() {
			now := time.Now()
			st.listResult = []store.PackageContainer{
				{PackageID: pkgID, Alias: "backend", UpstreamRepo: "ns/backend", Source: "manifest", CreatedAt: now, UpdatedAt: now},
				{PackageID: pkgID, Alias: "worker", UpstreamRepo: "ns/worker", Source: "", CreatedAt: now, UpdatedAt: now},
			}
			resp := listContainers()
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			b, _ := io.ReadAll(resp.Body)
			var got []struct {
				Alias        string `json:"alias"`
				UpstreamRepo string `json:"upstream_repo"`
				Source       string `json:"source"`
			}
			Expect(json.Unmarshal(b, &got)).To(Succeed())
			Expect(got).To(HaveLen(2))
		})
	})

	Describe("DELETE /packages/{id}/containers/{alias}", func() {
		It("forwards the (id, alias) tuple to the store", func() {
			resp := deleteContainer("backend")
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
			Expect(st.deletes).To(HaveLen(1))
			Expect(st.deletes[0]).To(Equal(deletedContainer{PackageID: pkgID, Alias: "backend"}))
		})
	})
})
