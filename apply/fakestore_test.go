package apply_test

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
)

// fakeStore is a minimal in-memory DataStore for apply tests. Only the
// methods the reconciler exercises are implemented; the rest panic so an
// accidental dependency on real DB behavior shows up loud.
type fakeStore struct {
	mu sync.Mutex

	upstreamByID map[uuid.UUID]*store.UpstreamCredential
	upstreamList []*store.UpstreamCredential // preserves insertion order

	packagesByID map[uuid.UUID]*store.Package
	packagesList []*store.Package

	licensesByID    map[uuid.UUID]*store.License
	licensesList    []*store.License
	grantsByLicense map[uuid.UUID][]store.PackageGrant

	oidcByID map[uuid.UUID]*store.OIDCProvider
	oidcList []*store.OIDCProvider

	staticByID    map[uuid.UUID]*store.StaticAdmin
	staticByEmail map[string]*store.StaticAdmin
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		upstreamByID:    map[uuid.UUID]*store.UpstreamCredential{},
		packagesByID:    map[uuid.UUID]*store.Package{},
		licensesByID:    map[uuid.UUID]*store.License{},
		grantsByLicense: map[uuid.UUID][]store.PackageGrant{},
		oidcByID:        map[uuid.UUID]*store.OIDCProvider{},
		staticByID:      map[uuid.UUID]*store.StaticAdmin{},
		staticByEmail:   map[string]*store.StaticAdmin{},
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

// --- oidc providers ---------------------------------------------------------

func (s *fakeStore) ListOIDCProviders(context.Context) ([]store.OIDCProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.OIDCProvider, 0, len(s.oidcList))
	for _, p := range s.oidcList {
		out = append(out, *p)
	}
	return out, nil
}

func (s *fakeStore) InsertOIDCProvider(_ context.Context, o *store.OIDCProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	o.CreatedAt = time.Now()
	o.UpdatedAt = time.Now()
	cp := *o
	s.oidcByID[o.ID] = &cp
	s.oidcList = append(s.oidcList, &cp)
	return nil
}

func (s *fakeStore) DeleteOIDCProvider(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.oidcByID[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.oidcByID, id)
	out := s.oidcList[:0]
	for _, p := range s.oidcList {
		if p.ID != id {
			out = append(out, p)
		}
	}
	s.oidcList = out
	return nil
}

// --- static admins ---------------------------------------------------------

func (s *fakeStore) ListStaticAdmins(context.Context) ([]store.StaticAdmin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.StaticAdmin, 0, len(s.staticByID))
	for _, sa := range s.staticByID {
		out = append(out, *sa)
	}
	return out, nil
}

func (s *fakeStore) GetStaticAdminByEmail(_ context.Context, email string) (*store.StaticAdmin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sa, ok := s.staticByEmail[strings.ToLower(email)]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *sa
	return &cp, nil
}

func (s *fakeStore) UpsertStaticAdmin(_ context.Context, sa *store.StaticAdmin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	emailLower := strings.ToLower(sa.Email)
	if existing, ok := s.staticByEmail[emailLower]; ok {
		// Update in place.
		existing.PasswordHash = sa.PasswordHash
		existing.Source = sa.Source
		existing.UpdatedAt = time.Now()
		return nil
	}
	if sa.ID == uuid.Nil {
		sa.ID = uuid.New()
	}
	sa.CreatedAt = time.Now()
	sa.UpdatedAt = time.Now()
	cp := *sa
	s.staticByID[sa.ID] = &cp
	s.staticByEmail[emailLower] = &cp
	return nil
}

func (s *fakeStore) DeleteStaticAdmin(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sa, ok := s.staticByID[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(s.staticByID, id)
	delete(s.staticByEmail, strings.ToLower(sa.Email))
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

func (*fakeStore) GetOIDCProvider(context.Context, uuid.UUID) (*store.OIDCProvider, error) {
	panic("unused")
}
func (*fakeStore) GetOIDCProviderByName(context.Context, string) (*store.OIDCProvider, error) {
	panic("unused")
}

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
func (*fakeStore) AddContact(context.Context, *store.LicenseContact) error { panic("unused") }
func (*fakeStore) RemoveContact(context.Context, uuid.UUID, string) error  { panic("unused") }
func (*fakeStore) FindLicensesByContactEmail(context.Context, string) ([]store.License, error) {
	panic("unused")
}

func (*fakeStore) GrantedPackagesForLicense(context.Context, uuid.UUID) ([]store.Package, error) {
	panic("unused")
}
func (*fakeStore) HasGrant(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	panic("unused")
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
