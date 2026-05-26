package server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Server Suite")
}

// fakeStore implements store.DataStore well enough to drive the OCI flow
// without a real Postgres. Only the methods the handlers actually call are
// fleshed out; the rest panic so a missing implementation is loud.
type fakeStore struct {
	mu              sync.Mutex
	customerTokens  map[string]*store.CustomerToken // keyed by TokenID
	customerByID    map[uuid.UUID]*store.CustomerToken
	licenses        map[uuid.UUID]*store.License
	packagesByPath  map[string]*store.Package
	packagesBySlug  map[string]*store.Package
	packagesByID    map[uuid.UUID]*store.Package
	upstreamByID    map[uuid.UUID]*store.UpstreamCredential
	grants          map[grantKey]bool
	// containers keyed by (package-id, alias). Used by the OCI proxy and
	// token-mint paths to resolve multi-container packages.
	containers      map[containerKey]*store.PackageContainer
	touchedTokens   []uuid.UUID
	touchedUpstream []uuid.UUID
}

type containerKey struct {
	Package uuid.UUID
	Alias   string
}

type grantKey struct {
	License uuid.UUID
	Package uuid.UUID
	Action  string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		customerTokens: map[string]*store.CustomerToken{},
		customerByID:   map[uuid.UUID]*store.CustomerToken{},
		licenses:       map[uuid.UUID]*store.License{},
		packagesByPath: map[string]*store.Package{},
		packagesBySlug: map[string]*store.Package{},
		packagesByID:   map[uuid.UUID]*store.Package{},
		upstreamByID:   map[uuid.UUID]*store.UpstreamCredential{},
		grants:         map[grantKey]bool{},
		containers:     map[containerKey]*store.PackageContainer{},
	}
}

func (s *fakeStore) GetCustomerTokenByTokenID(_ context.Context, tokenID string) (*store.CustomerToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.customerTokens[tokenID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return t, nil
}

func (s *fakeStore) GetCustomerToken(_ context.Context, id uuid.UUID) (*store.CustomerToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.customerByID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return t, nil
}

func (s *fakeStore) GetLicense(_ context.Context, id uuid.UUID) (*store.License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.licenses[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return l, nil
}

func (s *fakeStore) GetPackageByPath(_ context.Context, p string) (*store.Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pkg, ok := s.packagesByPath[p]
	if !ok {
		return nil, store.ErrNotFound
	}
	return pkg, nil
}

func (s *fakeStore) GetPackage(_ context.Context, id uuid.UUID) (*store.Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pkg, ok := s.packagesByID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return pkg, nil
}

func (s *fakeStore) GetUpstreamCredential(_ context.Context, id uuid.UUID) (*store.UpstreamCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.upstreamByID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return c, nil
}

func (s *fakeStore) HasGrant(_ context.Context, licenseID, packageID uuid.UUID, action string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grants[grantKey{licenseID, packageID, action}], nil
}

func (s *fakeStore) TouchCustomerToken(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchedTokens = append(s.touchedTokens, id)
	return nil
}

func (s *fakeStore) TouchUpstreamCredential(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchedUpstream = append(s.touchedUpstream, id)
	return nil
}

// --- Unused methods: panic to surface accidental dependencies in tests. -----

func (*fakeStore) GetUserByEmail(context.Context, string) (*store.User, error) {
	panic("unused")
}
func (*fakeStore) GetUserByOIDC(context.Context, uuid.UUID, string) (*store.User, error) {
	panic("unused")
}
func (*fakeStore) InsertUser(context.Context, *store.User) error   { panic("unused") }
func (*fakeStore) UpdateUser(context.Context, *store.User) error   { panic("unused") }
func (*fakeStore) ListUsers(context.Context) ([]store.User, error) { panic("unused") }
func (*fakeStore) CountUsers(context.Context) (int, error)         { panic("unused") }
func (*fakeStore) ListOIDCProviders(context.Context) ([]store.OIDCProvider, error) {
	panic("unused")
}
func (*fakeStore) GetOIDCProvider(context.Context, uuid.UUID) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*fakeStore) GetOIDCProviderByName(context.Context, string) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*fakeStore) InsertOIDCProvider(context.Context, *store.OIDCProvider) error { panic("unused") }
func (*fakeStore) DeleteOIDCProvider(context.Context, uuid.UUID) error           { panic("unused") }
func (*fakeStore) ListUpstreamCredentials(context.Context) ([]store.UpstreamCredential, error) {
	panic("unused")
}
func (*fakeStore) InsertUpstreamCredential(context.Context, *store.UpstreamCredential) error {
	panic("unused")
}
func (*fakeStore) DeleteUpstreamCredential(context.Context, uuid.UUID) error { panic("unused") }
func (*fakeStore) ListPackages(context.Context) ([]store.Package, error)     { panic("unused") }
func (s *fakeStore) GetPackageBySlug(_ context.Context, slug string) (*store.Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.packagesBySlug[slug]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}
func (*fakeStore) InsertPackage(context.Context, *store.Package) error   { panic("unused") }
func (*fakeStore) UpdatePackage(context.Context, *store.Package) error   { panic("unused") }
func (*fakeStore) DeletePackage(context.Context, uuid.UUID) error        { panic("unused") }
func (*fakeStore) ListLicenses(context.Context) ([]store.License, error) { panic("unused") }
func (*fakeStore) GetLicenseByLicenseID(context.Context, string) (*store.License, error) {
	panic("unused")
}
func (*fakeStore) InsertLicense(context.Context, *store.License) error { panic("unused") }
func (*fakeStore) RevokeLicense(context.Context, uuid.UUID) error      { panic("unused") }
func (*fakeStore) DeleteLicense(context.Context, uuid.UUID) error      { panic("unused") }
func (*fakeStore) SetLicenseCustomerRotate(context.Context, uuid.UUID, bool) error {
	panic("unused")
}
func (*fakeStore) ListCustomerTokens(context.Context, *uuid.UUID) ([]store.CustomerToken, error) {
	panic("unused")
}
func (*fakeStore) InsertCustomerToken(context.Context, *store.CustomerToken) error {
	panic("unused")
}
func (*fakeStore) RevokeCustomerToken(context.Context, uuid.UUID) error { panic("unused") }
func (*fakeStore) CountActiveCustomerTokens(context.Context) (int, error) {
	panic("unused")
}
func (*fakeStore) ListActiveCustomerTokenForLicense(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	return nil, store.ErrNotFound
}
func (*fakeStore) RotateCustomerTokenForLicense(context.Context, uuid.UUID, *uuid.UUID, string, string, string) (uuid.UUID, error) {
	panic("unused")
}
func (*fakeStore) ListGrantsForLicense(context.Context, uuid.UUID) ([]store.PackageGrant, error) {
	panic("unused")
}
func (*fakeStore) GrantedPackagesForLicense(context.Context, uuid.UUID) ([]store.Package, error) {
	panic("unused")
}
func (*fakeStore) ReplaceGrantsForLicense(context.Context, uuid.UUID, []uuid.UUID, []string) error {
	panic("unused")
}
func (*fakeStore) ListStaticAdmins(context.Context) ([]store.StaticAdmin, error) {
	return nil, nil
}
func (*fakeStore) GetStaticAdminByEmail(context.Context, string) (*store.StaticAdmin, error) {
	return nil, store.ErrNotFound
}
func (*fakeStore) UpsertStaticAdmin(context.Context, *store.StaticAdmin) error { panic("unused") }
func (*fakeStore) DeleteStaticAdmin(context.Context, uuid.UUID) error          { panic("unused") }

// Root-key stubs — added when license issuance moved into artifact-gateway.
func (*fakeStore) ListRootKeys(context.Context) ([]store.RootKey, error)         { panic("unused") }
func (*fakeStore) GetRootKey(context.Context, uuid.UUID) (*store.RootKey, error) { panic("unused") }
func (*fakeStore) GetRootKeyByFingerprint(context.Context, string) (*store.RootKey, error) {
	panic("unused")
}
func (*fakeStore) GetActiveSigningKey(context.Context) (*store.RootKey, error) { panic("unused") }
func (*fakeStore) InsertRootKey(context.Context, *store.RootKey) error         { panic("unused") }
func (*fakeStore) SetActiveRootKey(context.Context, uuid.UUID) error           { panic("unused") }
func (*fakeStore) DeleteRootKey(context.Context, uuid.UUID) error              { panic("unused") }

// Package-container impls — multi-container routing tests exercise these.
func (s *fakeStore) ListContainersForPackage(_ context.Context, pkgID uuid.UUID) ([]store.PackageContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.PackageContainer, 0)
	for k, v := range s.containers {
		if k.Package == pkgID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (*fakeStore) ListManifestContainersForPackage(context.Context, uuid.UUID) ([]store.PackageContainer, error) {
	panic("unused")
}
func (s *fakeStore) GetContainer(_ context.Context, pkgID uuid.UUID, alias string) (*store.PackageContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.containers[containerKey{pkgID, alias}]
	if !ok {
		return nil, store.ErrNotFound
	}
	return c, nil
}
func (*fakeStore) UpsertContainer(context.Context, *store.PackageContainer) error { panic("unused") }
func (*fakeStore) DeleteContainer(context.Context, uuid.UUID, string) error       { panic("unused") }
func (*fakeStore) ReplaceManifestContainersForPackage(context.Context, uuid.UUID, []store.PackageContainer) error {
	panic("unused")
}

// License-contact stubs.
func (*fakeStore) ListContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*fakeStore) ListManifestContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*fakeStore) AddContact(context.Context, *store.LicenseContact) error { panic("unused") }
func (*fakeStore) RemoveContact(context.Context, uuid.UUID, string) error  { panic("unused") }
func (*fakeStore) ReplaceManifestContactsForLicense(context.Context, uuid.UUID, []store.LicenseContact) error {
	panic("unused")
}
func (*fakeStore) FindLicensesByContactEmail(context.Context, string) ([]store.License, error) {
	panic("unused")
}

func (*fakeStore) InsertAuditEvent(audit.AuditEvent) error                     { return nil } // no-op so audit.Auditor doesn't blow up
func (*fakeStore) ListAuditEvents(context.Context, int, *time.Time) ([]audit.AuditEvent, error) {
	panic("unused")
}

// Branding stubs — server tests don't exercise the runtime branding surface.
func (*fakeStore) GetBranding(context.Context) (*store.Branding, error) { panic("unused") }
func (*fakeStore) SetBranding(context.Context, *store.Branding) error   { panic("unused") }

func (*fakeStore) Close() {}

// Compile-time check: fakeStore satisfies store.DataStore.
var _ store.DataStore = (*fakeStore)(nil)
