package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	cnaklicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// configApplyFakeVerifier echoes the blob's leading segment back as the
// license ID so we can drive the reconciler without a real signer.
type configApplyFakeVerifier struct{}

func (configApplyFakeVerifier) VerifyLicenseBlob(raw string) (*cnaklicense.License, error) {
	parts := strings.SplitN(raw, "|", 4)
	if len(parts) < 2 {
		return nil, errors.New("malformed test license")
	}
	return &cnaklicense.License{ID: parts[0], Customer: parts[1], Tier: "enterprise"}, nil
}

// configApplyFakeStore is the smallest DataStore the apply path needs:
// upstream credentials, packages, licenses, and grants. We inject
// insertCredErr to force the second of two credentials to fail so the
// resulting ApplyReport has a populated Errors slice and the handler must
// emit 207 instead of 200.
type configApplyFakeStore struct {
	mu sync.Mutex

	creds           []store.UpstreamCredential
	packages        []store.Package
	licenses        []store.License
	grants          map[uuid.UUID][]store.PackageGrant
	containersByPkg map[uuid.UUID][]store.PackageContainer
	insertCredErrOn string // name of credential to fail on insert
	insertedCredsOK int
}

func (s *configApplyFakeStore) ListUpstreamCredentials(context.Context) ([]store.UpstreamCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]store.UpstreamCredential(nil), s.creds...)
	return out, nil
}

func (s *configApplyFakeStore) InsertUpstreamCredential(_ context.Context, c *store.UpstreamCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertCredErrOn != "" && c.Name == s.insertCredErrOn {
		return errors.New("simulated insert failure for " + c.Name)
	}
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	s.creds = append(s.creds, *c)
	s.insertedCredsOK++
	return nil
}

func (s *configApplyFakeStore) DeleteUpstreamCredential(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.creds {
		if c.ID == id {
			s.creds = append(s.creds[:i], s.creds[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *configApplyFakeStore) ListPackages(context.Context) ([]store.Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.Package(nil), s.packages...), nil
}

func (s *configApplyFakeStore) InsertPackage(_ context.Context, p *store.Package) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	if p.Source == "" {
		p.Source = "oci"
	}
	p.CreatedAt = time.Now()
	p.UpdatedAt = time.Now()
	s.packages = append(s.packages, *p)
	return nil
}

func (s *configApplyFakeStore) UpdatePackage(_ context.Context, p *store.Package) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ep := range s.packages {
		if ep.ID == p.ID {
			s.packages[i] = *p
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *configApplyFakeStore) DeletePackage(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.packages {
		if p.ID == id {
			s.packages = append(s.packages[:i], s.packages[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *configApplyFakeStore) ListLicenses(context.Context) ([]store.License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.License(nil), s.licenses...), nil
}

func (s *configApplyFakeStore) InsertLicense(_ context.Context, l *store.License) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	l.CreatedAt = time.Now()
	l.UpdatedAt = time.Now()
	s.licenses = append(s.licenses, *l)
	return nil
}

func (s *configApplyFakeStore) DeleteLicense(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.licenses {
		if l.ID == id {
			s.licenses = append(s.licenses[:i], s.licenses[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *configApplyFakeStore) ListGrantsForLicense(_ context.Context, lic uuid.UUID) ([]store.PackageGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.PackageGrant(nil), s.grants[lic]...), nil
}

func (s *configApplyFakeStore) ReplaceGrantsForLicense(_ context.Context, lic uuid.UUID, pkgIDs []uuid.UUID, actions []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grants == nil {
		s.grants = map[uuid.UUID][]store.PackageGrant{}
	}
	if len(pkgIDs) == 0 {
		delete(s.grants, lic)
		return nil
	}
	rows := make([]store.PackageGrant, 0, len(pkgIDs))
	for _, p := range pkgIDs {
		rows = append(rows, store.PackageGrant{LicenseID: lic, PackageID: p, Actions: actions})
	}
	s.grants[lic] = rows
	return nil
}

// Audit sink — no-op so the handler's LogResourceMutation calls don't blow up.
func (*configApplyFakeStore) InsertAuditEvent(audit.AuditEvent) error { return nil }

// Everything else panics — the apply handler must not reach these.
func (*configApplyFakeStore) GetUserByEmail(context.Context, string) (*store.User, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetUserByOIDC(context.Context, uuid.UUID, string) (*store.User, error) {
	panic("unused")
}
func (*configApplyFakeStore) InsertUser(context.Context, *store.User) error   { panic("unused") }
func (*configApplyFakeStore) UpdateUser(context.Context, *store.User) error   { panic("unused") }
func (*configApplyFakeStore) ListUsers(context.Context) ([]store.User, error) { panic("unused") }
func (*configApplyFakeStore) CountUsers(context.Context) (int, error)         { panic("unused") }
func (*configApplyFakeStore) ListOIDCProviders(context.Context) ([]store.OIDCProvider, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetOIDCProvider(context.Context, uuid.UUID) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetOIDCProviderByName(context.Context, string) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*configApplyFakeStore) InsertOIDCProvider(context.Context, *store.OIDCProvider) error {
	panic("unused")
}
func (*configApplyFakeStore) DeleteOIDCProvider(context.Context, uuid.UUID) error { panic("unused") }
func (*configApplyFakeStore) GetUpstreamCredential(context.Context, uuid.UUID) (*store.UpstreamCredential, error) {
	panic("unused")
}
func (*configApplyFakeStore) TouchUpstreamCredential(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*configApplyFakeStore) GetPackage(context.Context, uuid.UUID) (*store.Package, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetPackageByPath(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetPackageBySlug(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetLicense(context.Context, uuid.UUID) (*store.License, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetLicenseByLicenseID(context.Context, string) (*store.License, error) {
	panic("unused")
}
func (*configApplyFakeStore) RevokeLicense(context.Context, uuid.UUID) error { panic("unused") }
func (*configApplyFakeStore) ListCustomerTokens(context.Context, *uuid.UUID) ([]store.CustomerToken, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetCustomerToken(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetCustomerTokenByTokenID(context.Context, string) (*store.CustomerToken, error) {
	panic("unused")
}
func (*configApplyFakeStore) InsertCustomerToken(context.Context, *store.CustomerToken) error {
	panic("unused")
}
func (*configApplyFakeStore) RevokeCustomerToken(context.Context, uuid.UUID) error {
	panic("unused")
}
func (*configApplyFakeStore) TouchCustomerToken(context.Context, uuid.UUID) error  { panic("unused") }
func (*configApplyFakeStore) CountActiveCustomerTokens(context.Context) (int, error) { panic("unused") }
func (*configApplyFakeStore) GrantedPackagesForLicense(context.Context, uuid.UUID) ([]store.Package, error) {
	panic("unused")
}
func (*configApplyFakeStore) HasGrant(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	panic("unused")
}
func (*configApplyFakeStore) ListAuditEvents(context.Context, int, *time.Time) ([]audit.AuditEvent, error) {
	panic("unused")
}
func (*configApplyFakeStore) ListStaticAdmins(context.Context) ([]store.StaticAdmin, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetStaticAdminByEmail(context.Context, string) (*store.StaticAdmin, error) {
	return nil, store.ErrNotFound
}
func (*configApplyFakeStore) UpsertStaticAdmin(context.Context, *store.StaticAdmin) error {
	panic("unused")
}
func (*configApplyFakeStore) DeleteStaticAdmin(context.Context, uuid.UUID) error { panic("unused") }
func (*configApplyFakeStore) ListRootKeys(context.Context) ([]store.RootKey, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetRootKey(context.Context, uuid.UUID) (*store.RootKey, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetRootKeyByFingerprint(context.Context, string) (*store.RootKey, error) {
	panic("unused")
}
func (*configApplyFakeStore) GetActiveSigningKey(context.Context) (*store.RootKey, error) {
	panic("unused")
}
func (*configApplyFakeStore) InsertRootKey(context.Context, *store.RootKey) error { panic("unused") }
func (*configApplyFakeStore) SetActiveRootKey(context.Context, uuid.UUID) error   { panic("unused") }
func (*configApplyFakeStore) DeleteRootKey(context.Context, uuid.UUID) error      { panic("unused") }
func (*configApplyFakeStore) ListContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*configApplyFakeStore) ListManifestContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	return nil, nil
}
func (*configApplyFakeStore) AddContact(context.Context, *store.LicenseContact) error {
	panic("unused")
}
func (*configApplyFakeStore) RemoveContact(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (*configApplyFakeStore) ReplaceManifestContactsForLicense(context.Context, uuid.UUID, []store.LicenseContact) error {
	return nil
}
func (*configApplyFakeStore) FindLicensesByContactEmail(context.Context, string) ([]store.License, error) {
	panic("unused")
}

// Package-container stubs. The reconciler always invokes
// ListManifestContainersForPackage and ReplaceManifestContainersForPackage on
// every package spec. We record the last replace call so a test can assert
// the (package, container set) tuple the handler forwarded.
func (s *configApplyFakeStore) ListContainersForPackage(_ context.Context, pkg uuid.UUID) ([]store.PackageContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]store.PackageContainer(nil), s.containersByPkg[pkg]...)
	return out, nil
}
func (s *configApplyFakeStore) ListManifestContainersForPackage(_ context.Context, pkg uuid.UUID) ([]store.PackageContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.PackageContainer, 0)
	for _, c := range s.containersByPkg[pkg] {
		if c.Source == "manifest" {
			out = append(out, c)
		}
	}
	return out, nil
}
func (*configApplyFakeStore) GetContainer(context.Context, uuid.UUID, string) (*store.PackageContainer, error) {
	panic("unused")
}
func (*configApplyFakeStore) UpsertContainer(context.Context, *store.PackageContainer) error {
	panic("unused")
}
func (*configApplyFakeStore) DeleteContainer(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (s *configApplyFakeStore) ReplaceManifestContainersForPackage(_ context.Context, pkg uuid.UUID, containers []store.PackageContainer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.containersByPkg == nil {
		s.containersByPkg = map[uuid.UUID][]store.PackageContainer{}
	}
	kept := s.containersByPkg[pkg][:0]
	for _, c := range s.containersByPkg[pkg] {
		if c.Source != "manifest" {
			kept = append(kept, c)
		}
	}
	for _, c := range containers {
		cp := c
		cp.Source = "manifest"
		cp.CreatedAt = time.Now()
		cp.UpdatedAt = time.Now()
		kept = append(kept, cp)
	}
	s.containersByPkg[pkg] = kept
	return nil
}
func (*configApplyFakeStore) GetBranding(context.Context) (*store.Branding, error) {
	panic("unused")
}
func (*configApplyFakeStore) SetBranding(context.Context, *store.Branding) error { panic("unused") }
func (*configApplyFakeStore) Close()                                             {}

var _ store.DataStore = (*configApplyFakeStore)(nil)

func deterministicKEK() string {
	var k [32]byte
	for i := range k {
		k[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(k[:])
}

var _ = Describe("config apply handler", func() {
	var (
		st     *configApplyFakeStore
		srv    *httptest.Server
		client *http.Client
	)

	BeforeEach(func() {
		st = &configApplyFakeStore{insertCredErrOn: "broken"}
		crypto, err := auth.NewCrypto(deterministicKEK())
		Expect(err).NotTo(HaveOccurred())
		auditor := audit.NewAuditor(nil, st, slog.Default())

		var verifier license.Verifier = configApplyFakeVerifier{}
		deps := AdminDeps{
			Store:    st,
			Crypto:   crypto,
			Verifier: verifier,
			Auditor:  auditor,
			Logger:   slog.Default(),
		}
		r := chi.NewRouter()
		r.Route("/api/v1/config", func(r chi.Router) {
			r.Post("/apply", handleConfigApply(deps))
		})
		srv = httptest.NewServer(r)
		client = srv.Client()
	})

	AfterEach(func() { srv.Close() })

	postApply := func(yaml string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/config/apply", bytes.NewBufferString(yaml))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/yaml")
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	It("returns 207 Multi-Status when the reconciler reports per-item errors", func() {
		// "broken" is wired to fail at InsertUpstreamCredential, so the
		// resulting report will have at least one entry in Errors.
		manifest := `
apiVersion: artifact-gateway.cnak.us/v1
kind: ArtifactGatewayConfig
metadata:
  name: test
spec:
  upstreamCredentials:
    - name: good
      kind: ghcr
      username: bot
      pat: tok
    - name: broken
      kind: ghcr
      username: bot
      pat: tok
`
		resp := postApply(manifest)
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusMultiStatus))

		b, _ := io.ReadAll(resp.Body)
		var report struct {
			DryRun bool `json:"dry_run"`
			Items  []struct {
				Kind, Name, Action string
			} `json:"items"`
			Errors []struct {
				Kind, Name, Message string
			} `json:"errors"`
		}
		Expect(json.Unmarshal(b, &report)).To(Succeed())
		Expect(report.Errors).NotTo(BeEmpty())
		// The "good" credential should still have made it through.
		Expect(st.insertedCredsOK).To(Equal(1))

		var brokenErr bool
		for _, e := range report.Errors {
			if e.Name == "broken" {
				brokenErr = true
			}
		}
		Expect(brokenErr).To(BeTrue())
	})

	It("returns 200 OK on a fully-successful apply (no Errors)", func() {
		manifest := `
apiVersion: artifact-gateway.cnak.us/v1
kind: ArtifactGatewayConfig
metadata:
  name: test
spec:
  upstreamCredentials:
    - name: good
      kind: ghcr
      username: bot
      pat: tok
`
		resp := postApply(manifest)
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("persists package containers declared under packages[].containers", func() {
		manifest := `
apiVersion: artifact-gateway.cnak.us/v1
kind: ArtifactGatewayConfig
metadata:
  name: test
spec:
  upstreamCredentials:
    - name: good
      kind: ghcr
      username: bot
      pat: tok
  packages:
    - slug: cnak-platform
      source: oci
      path: cnak-platform
      upstreamCredential: good
      kind: container
      containers:
        - alias: backend
          upstreamRepo: cnak-us/backend
        - alias: worker
          upstreamRepo: cnak-us/worker
          displayName: Worker
`
		resp := postApply(manifest)
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		// One package row should be present and its upstream_repo cleared
		// because the spec uses containers[].
		Expect(st.packages).To(HaveLen(1))
		Expect(st.packages[0].UpstreamRepo).To(Equal(""))

		// And the container rows are present, tagged manifest.
		got := st.containersByPkg[st.packages[0].ID]
		Expect(got).To(HaveLen(2))
		aliases := []string{}
		for _, c := range got {
			aliases = append(aliases, c.Alias)
			Expect(c.Source).To(Equal("manifest"))
		}
		Expect(aliases).To(ConsistOf("backend", "worker"))
	})
})
