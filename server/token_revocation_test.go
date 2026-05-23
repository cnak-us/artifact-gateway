package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
)

// fakeTokenStore satisfies the subset of store.DataStore that
// TokenRevocationChecker actually uses: GetCustomerToken. All other methods
// would only be invoked if the checker is wired into BearerJWT, which this
// test does not.
type fakeTokenStore struct {
	tokens map[uuid.UUID]*store.CustomerToken
	calls  int
}

func (f *fakeTokenStore) GetCustomerToken(_ context.Context, id uuid.UUID) (*store.CustomerToken, error) {
	f.calls++
	t, ok := f.tokens[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return t, nil
}

// TestTokenRevocationCheckerHitsThenCaches verifies the checker reads the
// store on first call, caches the result within TTL, and re-reads when the
// epoch is bumped (i.e. after a rotation).
func TestTokenRevocationChecker(t *testing.T) {
	st := &fakeTokenStore{tokens: map[uuid.UUID]*store.CustomerToken{}}
	active := uuid.New()
	revokedAt := time.Now()
	revoked := uuid.New()
	st.tokens[active] = &store.CustomerToken{ID: active, TokenID: "ACTIVE"}
	st.tokens[revoked] = &store.CustomerToken{ID: revoked, TokenID: "REVOKED", RevokedAt: &revokedAt}

	c := NewTokenRevocationChecker(storeAdapter{f: st}, 5*time.Second)

	rev, err := c.IsRevoked(context.Background(), active)
	if err != nil || rev {
		t.Fatalf("active row: revoked=%v err=%v", rev, err)
	}
	if st.calls != 1 {
		t.Fatalf("first call should hit store; got calls=%d", st.calls)
	}
	// Second call within TTL: cached, no extra store call.
	rev, err = c.IsRevoked(context.Background(), active)
	if err != nil || rev {
		t.Fatalf("cached active row: revoked=%v err=%v", rev, err)
	}
	if st.calls != 1 {
		t.Fatalf("cached lookup should NOT hit store; got calls=%d", st.calls)
	}

	// Revoked row should return true and also cache.
	rev, err = c.IsRevoked(context.Background(), revoked)
	if err != nil || !rev {
		t.Fatalf("revoked row: revoked=%v err=%v", rev, err)
	}

	// Missing row treated as revoked.
	rev, err = c.IsRevoked(context.Background(), uuid.New())
	if err != nil || !rev {
		t.Fatalf("missing row: revoked=%v err=%v", rev, err)
	}

	// BumpEpoch invalidates everything — next call re-reads.
	before := st.calls
	c.BumpEpoch()
	rev, err = c.IsRevoked(context.Background(), active)
	if err != nil || rev {
		t.Fatalf("post-bump active: revoked=%v err=%v", rev, err)
	}
	if st.calls != before+1 {
		t.Fatalf("post-bump should re-read; calls went %d -> %d", before, st.calls)
	}

	// Nil UUID short-circuits as revoked without touching the store.
	before = st.calls
	rev, err = c.IsRevoked(context.Background(), uuid.Nil)
	if err != nil || !rev {
		t.Fatalf("nil uuid: revoked=%v err=%v", rev, err)
	}
	if st.calls != before {
		t.Fatalf("nil uuid should not hit store; calls went %d -> %d", before, st.calls)
	}
}

// storeAdapter lets the test pass a partial fake into a DataStore-typed
// constructor. Only GetCustomerToken is exercised by TokenRevocationChecker;
// everything else panics so an accidental call surfaces loudly.
type storeAdapter struct{ f *fakeTokenStore }

func (s storeAdapter) GetCustomerToken(ctx context.Context, id uuid.UUID) (*store.CustomerToken, error) {
	return s.f.GetCustomerToken(ctx, id)
}
func (storeAdapter) GetUserByEmail(context.Context, string) (*store.User, error)             { panic("unused") }
func (storeAdapter) GetUserByOIDC(context.Context, uuid.UUID, string) (*store.User, error)   { panic("unused") }
func (storeAdapter) InsertUser(context.Context, *store.User) error                           { panic("unused") }
func (storeAdapter) UpdateUser(context.Context, *store.User) error                           { panic("unused") }
func (storeAdapter) ListUsers(context.Context) ([]store.User, error)                         { panic("unused") }
func (storeAdapter) CountUsers(context.Context) (int, error)                                 { panic("unused") }
func (storeAdapter) ListOIDCProviders(context.Context) ([]store.OIDCProvider, error)         { panic("unused") }
func (storeAdapter) GetOIDCProvider(context.Context, uuid.UUID) (*store.OIDCProvider, error) { panic("unused") }
func (storeAdapter) GetOIDCProviderByName(context.Context, string) (*store.OIDCProvider, error) {
	panic("unused")
}
func (storeAdapter) InsertOIDCProvider(context.Context, *store.OIDCProvider) error      { panic("unused") }
func (storeAdapter) DeleteOIDCProvider(context.Context, uuid.UUID) error                { panic("unused") }
func (storeAdapter) ListUpstreamCredentials(context.Context) ([]store.UpstreamCredential, error) {
	panic("unused")
}
func (storeAdapter) GetUpstreamCredential(context.Context, uuid.UUID) (*store.UpstreamCredential, error) {
	panic("unused")
}
func (storeAdapter) InsertUpstreamCredential(context.Context, *store.UpstreamCredential) error {
	panic("unused")
}
func (storeAdapter) DeleteUpstreamCredential(context.Context, uuid.UUID) error { panic("unused") }
func (storeAdapter) TouchUpstreamCredential(context.Context, uuid.UUID) error  { panic("unused") }
func (storeAdapter) ListPackages(context.Context) ([]store.Package, error)     { panic("unused") }
func (storeAdapter) GetPackage(context.Context, uuid.UUID) (*store.Package, error) {
	panic("unused")
}
func (storeAdapter) GetPackageByPath(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (storeAdapter) GetPackageBySlug(context.Context, string) (*store.Package, error) {
	panic("unused")
}
func (storeAdapter) InsertPackage(context.Context, *store.Package) error { panic("unused") }
func (storeAdapter) UpdatePackage(context.Context, *store.Package) error { panic("unused") }
func (storeAdapter) DeletePackage(context.Context, uuid.UUID) error      { panic("unused") }
func (storeAdapter) ListContainersForPackage(context.Context, uuid.UUID) ([]store.PackageContainer, error) {
	panic("unused")
}
func (storeAdapter) ListManifestContainersForPackage(context.Context, uuid.UUID) ([]store.PackageContainer, error) {
	panic("unused")
}
func (storeAdapter) GetContainer(context.Context, uuid.UUID, string) (*store.PackageContainer, error) {
	panic("unused")
}
func (storeAdapter) UpsertContainer(context.Context, *store.PackageContainer) error {
	panic("unused")
}
func (storeAdapter) DeleteContainer(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (storeAdapter) ReplaceManifestContainersForPackage(context.Context, uuid.UUID, []store.PackageContainer) error {
	panic("unused")
}
func (storeAdapter) ListLicenses(context.Context) ([]store.License, error) { panic("unused") }
func (storeAdapter) GetLicense(context.Context, uuid.UUID) (*store.License, error) {
	panic("unused")
}
func (storeAdapter) GetLicenseByLicenseID(context.Context, string) (*store.License, error) {
	panic("unused")
}
func (storeAdapter) InsertLicense(context.Context, *store.License) error { panic("unused") }
func (storeAdapter) RevokeLicense(context.Context, uuid.UUID) error      { panic("unused") }
func (storeAdapter) DeleteLicense(context.Context, uuid.UUID) error      { panic("unused") }
func (storeAdapter) ListCustomerTokens(context.Context, *uuid.UUID) ([]store.CustomerToken, error) {
	panic("unused")
}
func (storeAdapter) GetCustomerTokenByTokenID(context.Context, string) (*store.CustomerToken, error) {
	panic("unused")
}
func (storeAdapter) InsertCustomerToken(context.Context, *store.CustomerToken) error {
	panic("unused")
}
func (storeAdapter) RevokeCustomerToken(context.Context, uuid.UUID) error { panic("unused") }
func (storeAdapter) TouchCustomerToken(context.Context, uuid.UUID) error  { panic("unused") }
func (storeAdapter) CountActiveCustomerTokens(context.Context) (int, error) {
	panic("unused")
}
func (storeAdapter) ListActiveCustomerTokenForLicense(context.Context, uuid.UUID) (*store.CustomerToken, error) {
	panic("unused")
}
func (storeAdapter) RotateCustomerTokenForLicense(context.Context, uuid.UUID, *uuid.UUID, string, string, string) (uuid.UUID, error) {
	panic("unused")
}
func (storeAdapter) ListContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (storeAdapter) ListManifestContactsForLicense(context.Context, uuid.UUID) ([]store.LicenseContact, error) {
	panic("unused")
}
func (storeAdapter) AddContact(context.Context, *store.LicenseContact) error      { panic("unused") }
func (storeAdapter) RemoveContact(context.Context, uuid.UUID, string) error       { panic("unused") }
func (storeAdapter) ReplaceManifestContactsForLicense(context.Context, uuid.UUID, []store.LicenseContact) error {
	panic("unused")
}
func (storeAdapter) FindLicensesByContactEmail(context.Context, string) ([]store.License, error) {
	panic("unused")
}
func (storeAdapter) ListRootKeys(context.Context) ([]store.RootKey, error) { panic("unused") }
func (storeAdapter) GetRootKey(context.Context, uuid.UUID) (*store.RootKey, error) {
	panic("unused")
}
func (storeAdapter) GetRootKeyByFingerprint(context.Context, string) (*store.RootKey, error) {
	panic("unused")
}
func (storeAdapter) GetActiveSigningKey(context.Context) (*store.RootKey, error) {
	panic("unused")
}
func (storeAdapter) InsertRootKey(context.Context, *store.RootKey) error  { panic("unused") }
func (storeAdapter) SetActiveRootKey(context.Context, uuid.UUID) error    { panic("unused") }
func (storeAdapter) DeleteRootKey(context.Context, uuid.UUID) error       { panic("unused") }
func (storeAdapter) ListGrantsForLicense(context.Context, uuid.UUID) ([]store.PackageGrant, error) {
	panic("unused")
}
func (storeAdapter) GrantedPackagesForLicense(context.Context, uuid.UUID) ([]store.Package, error) {
	panic("unused")
}
func (storeAdapter) ReplaceGrantsForLicense(context.Context, uuid.UUID, []uuid.UUID, []string) error {
	panic("unused")
}
func (storeAdapter) HasGrant(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	panic("unused")
}
func (storeAdapter) ListStaticAdmins(context.Context) ([]store.StaticAdmin, error) {
	panic("unused")
}
func (storeAdapter) GetStaticAdminByEmail(context.Context, string) (*store.StaticAdmin, error) {
	panic("unused")
}
func (storeAdapter) UpsertStaticAdmin(context.Context, *store.StaticAdmin) error {
	panic("unused")
}
func (storeAdapter) DeleteStaticAdmin(context.Context, uuid.UUID) error { panic("unused") }
func (storeAdapter) GetBranding(context.Context) (*store.Branding, error) {
	panic("unused")
}
func (storeAdapter) SetBranding(context.Context, *store.Branding) error  { panic("unused") }
func (storeAdapter) InsertAuditEvent(audit.AuditEvent) error                          { panic("unused") }
func (storeAdapter) ListAuditEvents(context.Context, int, *time.Time) ([]audit.AuditEvent, error) {
	panic("unused")
}
func (storeAdapter) Close()                                                           { panic("unused") }

// TestRequireCustomHeader covers the CSRF defense-in-depth middleware.
func TestRequireCustomHeader(t *testing.T) {
	mw := RequireCustomHeader("X-Requested-With")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	// Missing header → 403, downstream not invoked.
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/x", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing header: got %d, want 403", w.Code)
	}
	if called {
		t.Fatal("downstream should not be invoked when header is missing")
	}

	// With header → passes through.
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.Header.Set("X-Requested-With", "fetch")
	mw(next).ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("with header: got %d, want 204", w.Code)
	}
	if !called {
		t.Fatal("downstream should be invoked")
	}
}

