package apply_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
)

// fakeStore is a minimal in-memory DataStore for apply tests. Only the
// methods the reconciler exercises are implemented; the rest panic so an
// accidental dependency on real DB behavior shows up loud.
//
// enforceFK toggles a foreign-key check on DeleteUpstreamCredential: with FK
// on, attempting to delete a credential while any package still references it
// returns an error (mirrors `ON DELETE RESTRICT` in pg). Tests opt in to this
// to verify prune ordering.
type fakeStore struct {
	mu sync.Mutex

	enforceFK bool

	upstreamByID map[uuid.UUID]*store.UpstreamCredential
	upstreamList []*store.UpstreamCredential // preserves insertion order

	packagesByID map[uuid.UUID]*store.Package
	packagesList []*store.Package

	licensesByID    map[uuid.UUID]*store.License
	licensesList    []*store.License
	grantsByLicense map[uuid.UUID][]store.PackageGrant

	// containersByPkg keeps rows in insertion order per package; tests
	// inspect the slice directly to check source tags survive reapply.
	containersByPkg map[uuid.UUID][]*store.PackageContainer
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		upstreamByID:    map[uuid.UUID]*store.UpstreamCredential{},
		packagesByID:    map[uuid.UUID]*store.Package{},
		licensesByID:    map[uuid.UUID]*store.License{},
		grantsByLicense: map[uuid.UUID][]store.PackageGrant{},
		containersByPkg: map[uuid.UUID][]*store.PackageContainer{},
	}
}

// --- upstream credentials ---------------------------------------------------

func (s *fakeStore) ListUpstreamCredentials(context.Context) ([]store.UpstreamCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.UpstreamCredential, 0, len(s.upstreamList))
	for _, c := range s.upstreamList {
		out = append(out, *c)
	}
	return out, nil
}

func (s *fakeStore) InsertUpstreamCredential(_ context.Context, c *store.UpstreamCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.UpdatedAt = time.Now()
	cp := *c
	s.upstreamByID[c.ID] = &cp
	s.upstreamList = append(s.upstreamList, &cp)
	return nil
}

func (s *fakeStore) DeleteUpstreamCredential(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.upstreamByID[id]; !ok {
		return store.ErrNotFound
	}
	if s.enforceFK {
		for _, p := range s.packagesList {
			if p.UpstreamCredentialID == id {
				return fmt.Errorf("fk violation: package %s still references credential", p.Slug)
			}
		}
	}
	delete(s.upstreamByID, id)
	out := s.upstreamList[:0]
	for _, c := range s.upstreamList {
		if c.ID != id {
			out = append(out, c)
		}
	}
	s.upstreamList = out
	return nil
}

// --- packages ---------------------------------------------------------------

func (s *fakeStore) ListPackages(context.Context) ([]store.Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Package, 0, len(s.packagesList))
	for _, p := range s.packagesList {
		out = append(out, *p)
	}
	return out, nil
}

func (s *fakeStore) InsertPackage(_ context.Context, p *store.Package) error {
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
	cp := *p
	s.packagesByID[p.ID] = &cp
	s.packagesList = append(s.packagesList, &cp)
	return nil
}

func (s *fakeStore) UpdatePackage(_ context.Context, p *store.Package) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.packagesByID[p.ID]; !ok {
		return store.ErrNotFound
	}
	p.UpdatedAt = time.Now()
	if p.Source == "" {
		p.Source = "oci"
	}
	cp := *p
	s.packagesByID[p.ID] = &cp
	for i, ep := range s.packagesList {
		if ep.ID == p.ID {
			s.packagesList[i] = &cp
		}
	}
	return nil
}

func (s *fakeStore) DeletePackage(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.packagesByID[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.packagesByID, id)
	out := s.packagesList[:0]
	for _, p := range s.packagesList {
		if p.ID != id {
			out = append(out, p)
		}
	}
	s.packagesList = out
	return nil
}

// --- licenses ---------------------------------------------------------------

func (s *fakeStore) ListLicenses(context.Context) ([]store.License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.License, 0, len(s.licensesList))
	for _, l := range s.licensesList {
		out = append(out, *l)
	}
	return out, nil
}

func (s *fakeStore) InsertLicense(_ context.Context, l *store.License) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	l.CreatedAt = time.Now()
	l.UpdatedAt = time.Now()
	cp := *l
	s.licensesByID[l.ID] = &cp
	s.licensesList = append(s.licensesList, &cp)
	return nil
}

func (s *fakeStore) DeleteLicense(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.licensesByID[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.licensesByID, id)
	delete(s.grantsByLicense, id)
	out := s.licensesList[:0]
	for _, l := range s.licensesList {
		if l.ID != id {
			out = append(out, l)
		}
	}
	s.licensesList = out
	return nil
}

func (s *fakeStore) ListGrantsForLicense(_ context.Context, lic uuid.UUID) ([]store.PackageGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]store.PackageGrant(nil), s.grantsByLicense[lic]...)
	return out, nil
}

func (s *fakeStore) ReplaceGrantsForLicense(_ context.Context, lic uuid.UUID, pkgIDs []uuid.UUID, actions []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(pkgIDs) == 0 {
		delete(s.grantsByLicense, lic)
		return nil
	}
	if len(actions) == 0 {
		actions = []string{"pull"}
	}
	rows := make([]store.PackageGrant, 0, len(pkgIDs))
	for _, p := range pkgIDs {
		rows = append(rows, store.PackageGrant{LicenseID: lic, PackageID: p, Actions: append([]string(nil), actions...)})
	}
	s.grantsByLicense[lic] = rows
	return nil
}

// --- everything else: unused, panic on call -------------------------------

func (*fakeStore) GetUserByEmail(context.Context, string) (*store.User, error) { panic("unused") }
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

func (*fakeStore) GetUpstreamCredential(context.Context, uuid.UUID) (*store.UpstreamCredential, error) {
	panic("unused")
}
func (*fakeStore) TouchUpstreamCredential(context.Context, uuid.UUID) error { panic("unused") }

func (*fakeStore) GetPackage(context.Context, uuid.UUID) (*store.Package, error) { panic("unused") }
func (*fakeStore) GetPackageByPath(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (*fakeStore) GetPackageBySlug(context.Context, string) (*store.Package, error) {
	panic("unused")
}

func (*fakeStore) GetLicense(context.Context, uuid.UUID) (*store.License, error) { panic("unused") }
func (*fakeStore) GetLicenseByLicenseID(context.Context, string) (*store.License, error) {
	panic("unused")
}
func (*fakeStore) RevokeLicense(context.Context, uuid.UUID) error { panic("unused") }

func (*fakeStore) ListCustomerTokens(context.Context, *uuid.UUID) ([]store.CustomerToken, error) {
	panic("unused")
}
func (*fakeStore) GetCustomerToken(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	panic("unused")
}
func (*fakeStore) GetCustomerTokenByTokenID(context.Context, string) (*store.CustomerToken, error) {
	panic("unused")
}
func (*fakeStore) InsertCustomerToken(context.Context, *store.CustomerToken) error { panic("unused") }
func (*fakeStore) RevokeCustomerToken(context.Context, uuid.UUID) error            { panic("unused") }
func (*fakeStore) TouchCustomerToken(context.Context, uuid.UUID) error             { panic("unused") }
func (*fakeStore) CountActiveCustomerTokens(context.Context) (int, error)          { panic("unused") }

func (*fakeStore) ListContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (*fakeStore) ListManifestContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	return nil, nil
}
func (*fakeStore) AddContact(context.Context, *store.LicenseContact) error { panic("unused") }
func (*fakeStore) RemoveContact(context.Context, uuid.UUID, string) error  { panic("unused") }
func (*fakeStore) ReplaceManifestContactsForLicense(context.Context, uuid.UUID, []store.LicenseContact) error {
	return nil
}
func (*fakeStore) FindLicensesByContactEmail(context.Context, string) ([]store.License, error) {
	panic("unused")
}

func (*fakeStore) GrantedPackagesForLicense(context.Context, uuid.UUID) ([]store.Package, error) {
	panic("unused")
}
func (*fakeStore) HasGrant(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	panic("unused")
}

// --- package containers ----------------------------------------------------
//
// Real impls: the reconciler diffs against ListManifestContainersForPackage
// and writes via ReplaceManifestContainersForPackage. UI-owned rows can be
// seeded directly via UpsertContainer so tests can verify they survive a
// manifest reapply.

func (s *fakeStore) ListContainersForPackage(_ context.Context, pkg uuid.UUID) ([]store.PackageContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.PackageContainer, 0, len(s.containersByPkg[pkg]))
	for _, c := range s.containersByPkg[pkg] {
		out = append(out, *c)
	}
	return out, nil
}

func (s *fakeStore) ListManifestContainersForPackage(_ context.Context, pkg uuid.UUID) ([]store.PackageContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.PackageContainer, 0)
	for _, c := range s.containersByPkg[pkg] {
		if c.Source == "manifest" {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (s *fakeStore) GetContainer(_ context.Context, pkg uuid.UUID, alias string) (*store.PackageContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.containersByPkg[pkg] {
		if c.Alias == alias {
			cp := *c
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *fakeStore) UpsertContainer(_ context.Context, c *store.PackageContainer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ex := range s.containersByPkg[c.PackageID] {
		if ex.Alias == c.Alias {
			ex.UpstreamRepo = c.UpstreamRepo
			if c.DisplayName != "" {
				ex.DisplayName = c.DisplayName
			}
			if c.Source != "" {
				ex.Source = c.Source
			}
			ex.UpdatedAt = time.Now()
			s.containersByPkg[c.PackageID][i] = ex
			return nil
		}
	}
	cp := *c
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = time.Now()
	s.containersByPkg[c.PackageID] = append(s.containersByPkg[c.PackageID], &cp)
	return nil
}

func (s *fakeStore) DeleteContainer(_ context.Context, pkg uuid.UUID, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.containersByPkg[pkg]
	for i, c := range list {
		if c.Alias == alias {
			s.containersByPkg[pkg] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *fakeStore) ReplaceManifestContainersForPackage(_ context.Context, pkg uuid.UUID, containers []store.PackageContainer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		kept = append(kept, &cp)
	}
	s.containersByPkg[pkg] = kept
	return nil
}

func (*fakeStore) ListRootKeys(context.Context) ([]store.RootKey, error)         { panic("unused") }
func (*fakeStore) GetRootKey(context.Context, uuid.UUID) (*store.RootKey, error) { panic("unused") }
func (*fakeStore) GetRootKeyByFingerprint(context.Context, string) (*store.RootKey, error) {
	panic("unused")
}
func (*fakeStore) GetActiveSigningKey(context.Context) (*store.RootKey, error) { panic("unused") }
func (*fakeStore) InsertRootKey(context.Context, *store.RootKey) error         { panic("unused") }
func (*fakeStore) SetActiveRootKey(context.Context, uuid.UUID) error           { panic("unused") }
func (*fakeStore) DeleteRootKey(context.Context, uuid.UUID) error              { panic("unused") }

func (*fakeStore) ListStaticAdmins(context.Context) ([]store.StaticAdmin, error) {
	panic("unused")
}
func (*fakeStore) GetStaticAdminByEmail(context.Context, string) (*store.StaticAdmin, error) {
	panic("unused")
}
func (*fakeStore) UpsertStaticAdmin(context.Context, *store.StaticAdmin) error { panic("unused") }
func (*fakeStore) DeleteStaticAdmin(context.Context, uuid.UUID) error          { panic("unused") }

func (*fakeStore) InsertAuditEvent(audit.AuditEvent) error { return nil }
func (*fakeStore) ListAuditEvents(context.Context, int, *time.Time) ([]audit.AuditEvent, error) {
	panic("unused")
}

// Branding stubs — apply tests don't exercise the runtime branding surface.
func (*fakeStore) GetBranding(context.Context) (*store.Branding, error) { panic("unused") }
func (*fakeStore) SetBranding(context.Context, *store.Branding) error   { panic("unused") }

func (*fakeStore) Close() {}

// Compile-time assertion.
var _ store.DataStore = (*fakeStore)(nil)
