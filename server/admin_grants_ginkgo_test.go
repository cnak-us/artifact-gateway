package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// grantsFakeStore is a focused fake covering only the methods putGrants and
// listGrants actually touch. Everything else panics so accidental calls are
// loud. Kept distinct from the shared fakeStore so the assertions can inspect
// the exact (licenseID, packageIDs, actions) tuple the handler forwarded.
type grantsFakeStore struct {
	listResult []store.PackageGrant
	listErr    error

	replaceErr error

	replaceCalls   int
	lastLicenseID  uuid.UUID
	lastPackageIDs []uuid.UUID
	lastActions    []string
}

func (s *grantsFakeStore) ReplaceGrantsForLicense(_ context.Context, licenseID uuid.UUID, packageIDs []uuid.UUID, actions []string) error {
	s.replaceCalls++
	s.lastLicenseID = licenseID
	s.lastPackageIDs = packageIDs
	s.lastActions = actions
	return s.replaceErr
}

func (s *grantsFakeStore) ListGrantsForLicense(_ context.Context, _ uuid.UUID) ([]store.PackageGrant, error) {
	return s.listResult, s.listErr
}

// Audit sink no-op so audit.Auditor doesn't blow up on LogResourceMutation.
func (*grantsFakeStore) InsertAuditEvent(audit.AuditEvent) error { return nil }

// Every other DataStore method panics — putGrants/listGrants must not reach them.
func (*grantsFakeStore) GetUserByEmail(context.Context, string) (*store.User, error) {
	panic("unused")
}
func (*grantsFakeStore) GetUserByOIDC(context.Context, uuid.UUID, string) (*store.User, error) {
	panic("unused")
}
func (*grantsFakeStore) InsertUser(context.Context, *store.User) error   { panic("unused") }
func (*grantsFakeStore) UpdateUser(context.Context, *store.User) error   { panic("unused") }
func (*grantsFakeStore) ListUsers(context.Context) ([]store.User, error) { panic("unused") }
func (*grantsFakeStore) CountUsers(context.Context) (int, error)         { panic("unused") }
func (*grantsFakeStore) ListOIDCProviders(context.Context) ([]store.OIDCProvider, error) {
	panic("unused")
}
func (*grantsFakeStore) GetOIDCProvider(context.Context, uuid.UUID) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*grantsFakeStore) GetOIDCProviderByName(context.Context, string) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*grantsFakeStore) InsertOIDCProvider(context.Context, *store.OIDCProvider) error {
	panic("unused")
}
func (*grantsFakeStore) DeleteOIDCProvider(context.Context, uuid.UUID) error { panic("unused") }
func (*grantsFakeStore) ListUpstreamCredentials(context.Context) ([]store.UpstreamCredential, error) {
	panic("unused")
}
func (*grantsFakeStore) GetUpstreamCredential(context.Context, uuid.UUID) (*store.UpstreamCredential, error) {
	panic("unused")
}
func (*grantsFakeStore) InsertUpstreamCredential(context.Context, *store.UpstreamCredential) error {
	panic("unused")
}
func (*grantsFakeStore) DeleteUpstreamCredential(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*grantsFakeStore) TouchUpstreamCredential(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*grantsFakeStore) ListPackages(context.Context) ([]store.Package, error) { panic("unused") }
func (*grantsFakeStore) GetPackage(context.Context, uuid.UUID) (*store.Package, error) {
	panic("unused")
}
func (*grantsFakeStore) GetPackageByPath(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*grantsFakeStore) GetPackageBySlug(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*grantsFakeStore) InsertPackage(context.Context, *store.Package) error { panic("unused") }
func (*grantsFakeStore) UpdatePackage(context.Context, *store.Package) error { panic("unused") }
func (*grantsFakeStore) DeletePackage(context.Context, uuid.UUID) error      { panic("unused") }
func (*grantsFakeStore) ListLicenses(context.Context) ([]store.License, error) {
	panic("unused")
}
func (*grantsFakeStore) GetLicense(context.Context, uuid.UUID) (*store.License, error) {
	panic("unused")
}
func (*grantsFakeStore) GetLicenseByLicenseID(context.Context, string) (*store.License, error) {
	panic("unused")
}
func (*grantsFakeStore) InsertLicense(context.Context, *store.License) error { panic("unused") }
func (*grantsFakeStore) RevokeLicense(context.Context, uuid.UUID) error      { panic("unused") }
func (*grantsFakeStore) DeleteLicense(context.Context, uuid.UUID) error      { panic("unused") }
func (*grantsFakeStore) ListCustomerTokens(context.Context, *uuid.UUID) ([]store.CustomerToken, error) {
	panic("unused")
}
func (*grantsFakeStore) GetCustomerToken(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	panic("unused")
}
func (*grantsFakeStore) GetCustomerTokenByTokenID(context.Context, string) (*store.CustomerToken, error) {
	panic("unused")
}
func (*grantsFakeStore) InsertCustomerToken(context.Context, *store.CustomerToken) error {
	panic("unused")
}
func (*grantsFakeStore) RevokeCustomerToken(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*grantsFakeStore) TouchCustomerToken(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*grantsFakeStore) CountActiveCustomerTokens(context.Context) (int, error) { panic("unused") }
func (*grantsFakeStore) GrantedPackagesForLicense(context.Context, uuid.UUID) ([]store.Package, error) {
	panic("unused")
}
func (*grantsFakeStore) HasGrant(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	panic("unused")
}
func (*grantsFakeStore) ListAuditEvents(context.Context, int, *time.Time) ([]audit.AuditEvent, error) {
	panic("unused")
}
func (*grantsFakeStore) Close() {}

// Static-admin stubs — added when the declarative-CR work landed; this fake
// is intentionally narrow and panics on any call it doesn't expect.
func (*grantsFakeStore) ListStaticAdmins(context.Context) ([]store.StaticAdmin, error) {
	panic("unused")
}
func (*grantsFakeStore) GetStaticAdminByEmail(context.Context, string) (*store.StaticAdmin, error) {
	return nil, store.ErrNotFound
}
func (*grantsFakeStore) UpsertStaticAdmin(context.Context, *store.StaticAdmin) error {
	panic("unused")
}
func (*grantsFakeStore) DeleteStaticAdmin(context.Context, uuid.UUID) error { panic("unused") }

// Root-key stubs — added when license issuance moved into artifact-gateway.
func (*grantsFakeStore) ListRootKeys(context.Context) ([]store.RootKey, error) { panic("unused") }
func (*grantsFakeStore) GetRootKey(context.Context, uuid.UUID) (*store.RootKey, error) {
	panic("unused")
}
func (*grantsFakeStore) GetRootKeyByFingerprint(context.Context, string) (*store.RootKey, error) {
	panic("unused")
}
func (*grantsFakeStore) GetActiveSigningKey(context.Context) (*store.RootKey, error) {
	panic("unused")
}
func (*grantsFakeStore) InsertRootKey(context.Context, *store.RootKey) error { panic("unused") }
func (*grantsFakeStore) SetActiveRootKey(context.Context, uuid.UUID) error   { panic("unused") }
func (*grantsFakeStore) DeleteRootKey(context.Context, uuid.UUID) error      { panic("unused") }

// License-contact stubs.
func (*grantsFakeStore) ListContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*grantsFakeStore) ListManifestContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*grantsFakeStore) AddContact(context.Context, *store.LicenseContact) error { panic("unused") }
func (*grantsFakeStore) RemoveContact(context.Context, uuid.UUID, string) error  { panic("unused") }
func (*grantsFakeStore) ReplaceManifestContactsForLicense(context.Context, uuid.UUID, []store.LicenseContact) error {
	panic("unused")
}
func (*grantsFakeStore) FindLicensesByContactEmail(context.Context, string) ([]store.License, error) {
	panic("unused")
}

// Branding stubs — grants tests don't exercise the runtime branding surface.
func (*grantsFakeStore) GetBranding(context.Context) (*store.Branding, error) { panic("unused") }
func (*grantsFakeStore) SetBranding(context.Context, *store.Branding) error   { panic("unused") }

var _ store.DataStore = (*grantsFakeStore)(nil)

var _ = Describe("Admin grants handlers", func() {
	var (
		st      *grantsFakeStore
		auditor *audit.Auditor
		srv     *httptest.Server
		client  *http.Client
		licID   uuid.UUID
	)

	BeforeEach(func() {
		st = &grantsFakeStore{}
		auditor = audit.NewAuditor(nil, st, slog.Default())
		licID = uuid.New()

		// Mount a minimal router that registers ONLY the grants handlers under
		// test, bypassing the admin auth middleware. The handlers don't depend
		// on session context except for the audit username (nil session → "").
		r := chi.NewRouter()
		deps := AdminDeps{
			Store:   st,
			Auditor: auditor,
			Logger:  slog.Default(),
		}
		r.Route("/api/v1/licenses", func(r chi.Router) {
			r.Get("/{id}/grants", listGrants(deps))
			r.Put("/{id}/grants", putGrants(deps))
		})
		srv = httptest.NewServer(r)
		client = srv.Client()
	})

	AfterEach(func() {
		srv.Close()
	})

	putGrants := func(body string) *http.Response {
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/licenses/"+licID.String()+"/grants", bytes.NewBufferString(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	getGrants := func() *http.Response {
		resp, err := client.Get(srv.URL + "/api/v1/licenses/" + licID.String() + "/grants")
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	Describe("PUT /licenses/{id}/grants", func() {
		It("accepts the new {grants:[{package_id,actions}]} shape and persists it", func() {
			pkgID := uuid.New()
			body := `{"grants":[{"package_id":"` + pkgID.String() + `","actions":["pull"]}]}`

			resp := putGrants(body)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

			Expect(st.replaceCalls).To(Equal(1))
			Expect(st.lastLicenseID).To(Equal(licID))
			Expect(st.lastPackageIDs).To(Equal([]uuid.UUID{pkgID}))
			Expect(st.lastActions).To(ConsistOf("pull"))

			// And GET returns the same shape the UI expects.
			st.listResult = []store.PackageGrant{
				{LicenseID: licID, PackageID: pkgID, Actions: []string{"pull"}},
			}
			gresp := getGrants()
			defer gresp.Body.Close()
			Expect(gresp.StatusCode).To(Equal(http.StatusOK))
			b, _ := io.ReadAll(gresp.Body)
			var wrapped struct {
				Grants []struct {
					PackageID uuid.UUID `json:"package_id"`
					Actions   []string  `json:"actions"`
				} `json:"grants"`
			}
			Expect(json.Unmarshal(b, &wrapped)).To(Succeed())
			Expect(wrapped.Grants).To(HaveLen(1))
			Expect(wrapped.Grants[0].PackageID).To(Equal(pkgID))
			Expect(wrapped.Grants[0].Actions).To(ConsistOf("pull"))
		})

		It("accepts the legacy {package_ids,actions} shape and persists it", func() {
			pkgID := uuid.New()
			body := `{"package_ids":["` + pkgID.String() + `"],"actions":["pull"]}`

			resp := putGrants(body)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

			Expect(st.replaceCalls).To(Equal(1))
			Expect(st.lastPackageIDs).To(Equal([]uuid.UUID{pkgID}))
			Expect(st.lastActions).To(ConsistOf("pull"))
		})

		It("rejects an empty {} body and does not call the store", func() {
			resp := putGrants(`{}`)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))

			b, _ := io.ReadAll(resp.Body)
			msg := string(b)
			Expect(msg).To(SatisfyAny(
				ContainSubstring("grants"),
				ContainSubstring("package_ids"),
			))
			Expect(st.replaceCalls).To(Equal(0))
		})

		It("treats an explicit empty grants array as a clear", func() {
			resp := putGrants(`{"grants":[]}`)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

			Expect(st.replaceCalls).To(Equal(1))
			Expect(st.lastPackageIDs).To(BeEmpty())
		})

		It("dedupes the same package_id appearing twice", func() {
			pkgID := uuid.New()
			body := `{"grants":[` +
				`{"package_id":"` + pkgID.String() + `","actions":["pull"]},` +
				`{"package_id":"` + pkgID.String() + `","actions":["pull"]}` +
				`]}`

			resp := putGrants(body)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

			Expect(st.replaceCalls).To(Equal(1))
			Expect(st.lastPackageIDs).To(HaveLen(1))
			Expect(st.lastPackageIDs).To(Equal([]uuid.UUID{pkgID}))
		})

		It("drops grant entries whose package_id is the nil UUID", func() {
			realPkg := uuid.New()
			body := `{"grants":[` +
				`{"package_id":"00000000-0000-0000-0000-000000000000","actions":["pull"]},` +
				`{"package_id":"` + realPkg.String() + `","actions":["pull"]}` +
				`]}`

			resp := putGrants(body)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

			Expect(st.replaceCalls).To(Equal(1))
			Expect(st.lastPackageIDs).To(Equal([]uuid.UUID{realPkg}))
		})
	})
})
