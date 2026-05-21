// Package store defines the DataStore interface for artifact-gateway and
// provides a Postgres implementation. See pg.go.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/google/uuid"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

type User struct {
	ID             uuid.UUID
	Email          string
	PasswordHash   string // empty when OIDC-only
	OIDCSubject    string
	OIDCProviderID *uuid.UUID
	Role           string // admin | viewer
	DisabledAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type OIDCProvider struct {
	ID              uuid.UUID
	Name            string
	IssuerURL       string
	ClientID        string
	ClientSecretEnc []byte // KEK-wrapped
	Scopes          []string
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type UpstreamCredential struct {
	ID             uuid.UUID
	Name           string
	Kind           string // 'ghcr' | 'github-api' | 'oci-basic'
	Username       string
	PATEnc         []byte // KEK-wrapped
	PATFingerprint string // sha256 hex, first 8 chars shown in UI
	// BaseURL is the upstream host (scheme + host[:port], no trailing slash) used
	// by the proxy when Kind != 'ghcr'. For 'ghcr' it must be empty and the
	// process-wide Upstream.Host applies. For 'oci-basic' (and future bucket-B/C
	// kinds with self-hosted instances) it is required.
	BaseURL string
	// CABundlePEM is an optional PEM-encoded certificate chain trusted in
	// addition to the system roots when making outbound TLS connections to this
	// credential's BaseURL. Empty = use system roots only.
	CABundlePEM string
	// InsecureSkipTLSVerify disables upstream TLS certificate verification for
	// this credential. Only honor for non-production / lab use.
	InsecureSkipTLSVerify bool
	// IssuerKind tags bucket-C (cloud issuer-mint) credentials with the cloud
	// they target ('aws', 'gcp', 'azure'). Empty for non-issuer Kinds. Kept
	// separate from Kind so e.g. both 'ecr' and a future 'ecr-public' kind can
	// share IssuerKind='aws'.
	IssuerKind string
	// IssuerSecretEnc is the KEK-wrapped issuer secret (IAM keys JSON, GCP
	// SA JSON, AAD client secret JSON). Empty for non-issuer Kinds.
	IssuerSecretEnc []byte
	// IssuerConfigJSON is the non-secret per-cloud configuration (region,
	// tenant ID, registry hostname). Stored as JSON so future Kinds can add
	// fields without a migration.
	IssuerConfigJSON []byte
	LastUsedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Package struct {
	ID                    uuid.UUID
	Slug                  string
	Path                  string // /v2 repo path (e.g. cnak-us/cnak-core)
	UpstreamRepo          string // full ghcr path
	UpstreamCredentialID  uuid.UUID
	Kind                  string // container | helm | binary
	DisplayName           string
	Description           string
	ReleaseNotesURL       string
	InstallInstructionsMD string
	// Source discriminates between an OCI mirror and a GitHub Releases asset
	// source. "oci" (default) keeps the original /v2 proxy behavior; the
	// remaining fields below are only meaningful when Source == "github-release".
	Source         string // "oci" | "github-release"
	GitHubRepo     string // "owner/repo" — required when Source = "github-release"
	ReleasePattern string // "latest", a literal tag, or a semver constraint (caller-interpreted)
	AssetPattern   string // glob like "cnak-*-linux-amd64*"
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type License struct {
	ID           uuid.UUID
	LicenseID    string // the cnaklic-issued ID inside the .lic
	Customer     string
	Organization string
	Tier         string
	ExpiresAt    *time.Time
	LicBlob      string // raw .lic; re-verified on each token mint
	RevokedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Branding is the admin-editable white-label override stored in the singleton
// `branding` table. All fields are strings; an empty string means "not set"
// and the UI loader falls back to the compiled-in CNAK preset for that field.
// We chose strings over *string so the JSON shape is flat — the UI sends
// empty string for cleared fields.
type Branding struct {
	ProductName        string
	Vendor             string
	VendorShort        string
	FooterTagline      string
	EmbeddedTagline    string
	CatalogHeroEyebrow string
	HTMLTitle          string
	MetaDescription    string
	AccentLightMain    string
	AccentLightText    string
	AccentDarkMain     string
	AccentDarkText     string
	LogoSVG            string
	UpdatedAt          time.Time
	UpdatedBy          string
}

type CustomerToken struct {
	ID          uuid.UUID
	TokenID     string // 20-char base32; used as Basic-auth username
	SecretHash  string // bcrypt
	LicenseID   uuid.UUID
	Description string
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	LastUsedAt  *time.Time
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type LicenseContact struct {
	LicenseID uuid.UUID
	Email     string // canonical-lowered before write; CITEXT makes lookups case-insensitive
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RootKey is an Ed25519 signing key used to issue licenses. PrivateKeyEnc is
// KEK-wrapped (auth.Crypto); a NULL/empty PrivateKeyEnc means the row is
// verify-only (e.g. the legacy cnaklic pubkey imported by migration 00004).
// At most one row may have Active=true; that row signs newly-issued licenses.
type RootKey struct {
	ID            uuid.UUID
	Name          string
	PublicKey     []byte // raw 32-byte Ed25519 pubkey
	PrivateKeyEnc []byte // KEK-wrapped 64-byte ed25519.PrivateKey; nil for verify-only
	Fingerprint   string // first 16 hex chars of sha256(public_key)
	Active        bool
	ImportedFrom  string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// HasPrivateKey is a convenience for the admin DTO — true means the row can
// sign new licenses, false means it can only verify existing ones.
func (r *RootKey) HasPrivateKey() bool { return len(r.PrivateKeyEnc) > 0 }

type PackageGrant struct {
	LicenseID uuid.UUID
	PackageID uuid.UUID
	Actions   []string // ['pull']
}

// StaticAdmin is a break-glass admin written by the declarative manifest
// layer (or, in the future, by a dedicated admin endpoint). Authenticates
// password-only — no OIDC, no roles — and short-circuits the regular users
// table so it can survive a wiped users table. `Source` tags rows so the
// apply tool only prunes its own.
type StaticAdmin struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Source       string // 'manifest'; left open for future producers
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DataStore is the surface area the rest of the service depends on.
// pg.go provides the production implementation. Tests can use a fake.
type DataStore interface {
	// users
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByOIDC(ctx context.Context, providerID uuid.UUID, subject string) (*User, error)
	InsertUser(ctx context.Context, u *User) error
	UpdateUser(ctx context.Context, u *User) error
	ListUsers(ctx context.Context) ([]User, error)
	CountUsers(ctx context.Context) (int, error)

	// oidc providers
	ListOIDCProviders(ctx context.Context) ([]OIDCProvider, error)
	GetOIDCProvider(ctx context.Context, id uuid.UUID) (*OIDCProvider, error)
	GetOIDCProviderByName(ctx context.Context, name string) (*OIDCProvider, error)
	InsertOIDCProvider(ctx context.Context, p *OIDCProvider) error
	DeleteOIDCProvider(ctx context.Context, id uuid.UUID) error

	// upstream credentials
	ListUpstreamCredentials(ctx context.Context) ([]UpstreamCredential, error)
	GetUpstreamCredential(ctx context.Context, id uuid.UUID) (*UpstreamCredential, error)
	InsertUpstreamCredential(ctx context.Context, c *UpstreamCredential) error
	DeleteUpstreamCredential(ctx context.Context, id uuid.UUID) error
	TouchUpstreamCredential(ctx context.Context, id uuid.UUID) error

	// packages
	ListPackages(ctx context.Context) ([]Package, error)
	GetPackage(ctx context.Context, id uuid.UUID) (*Package, error)
	GetPackageByPath(ctx context.Context, path string) (*Package, error)
	GetPackageBySlug(ctx context.Context, slug string) (*Package, error)
	InsertPackage(ctx context.Context, p *Package) error
	UpdatePackage(ctx context.Context, p *Package) error
	DeletePackage(ctx context.Context, id uuid.UUID) error

	// licenses
	ListLicenses(ctx context.Context) ([]License, error)
	GetLicense(ctx context.Context, id uuid.UUID) (*License, error)
	GetLicenseByLicenseID(ctx context.Context, licID string) (*License, error)
	InsertLicense(ctx context.Context, l *License) error
	RevokeLicense(ctx context.Context, id uuid.UUID) error
	DeleteLicense(ctx context.Context, id uuid.UUID) error

	// customer tokens
	ListCustomerTokens(ctx context.Context, licenseID *uuid.UUID) ([]CustomerToken, error)
	GetCustomerToken(ctx context.Context, id uuid.UUID) (*CustomerToken, error)
	GetCustomerTokenByTokenID(ctx context.Context, tokenID string) (*CustomerToken, error)
	InsertCustomerToken(ctx context.Context, t *CustomerToken) error
	RevokeCustomerToken(ctx context.Context, id uuid.UUID) error
	TouchCustomerToken(ctx context.Context, id uuid.UUID) error
	CountActiveCustomerTokens(ctx context.Context) (int, error)

	// license contacts (federated-login allowlist)
	// ListContactsForLicense returns contacts ordered by created_at ASC; AddContact upserts
	// by (license_id, email) (lowercased) and preserves a non-empty name on conflict.
	ListContactsForLicense(ctx context.Context, licenseID uuid.UUID) ([]LicenseContact, error)
	AddContact(ctx context.Context, c *LicenseContact) error
	RemoveContact(ctx context.Context, licenseID uuid.UUID, email string) error
	FindLicensesByContactEmail(ctx context.Context, email string) ([]License, error)

	// root keys (signing keys for license issuance)
	ListRootKeys(ctx context.Context) ([]RootKey, error)
	GetRootKey(ctx context.Context, id uuid.UUID) (*RootKey, error)
	GetRootKeyByFingerprint(ctx context.Context, fp string) (*RootKey, error)
	GetActiveSigningKey(ctx context.Context) (*RootKey, error)
	InsertRootKey(ctx context.Context, k *RootKey) error
	SetActiveRootKey(ctx context.Context, id uuid.UUID) error
	DeleteRootKey(ctx context.Context, id uuid.UUID) error

	// package grants
	ListGrantsForLicense(ctx context.Context, licenseID uuid.UUID) ([]PackageGrant, error)
	GrantedPackagesForLicense(ctx context.Context, licenseID uuid.UUID) ([]Package, error)
	ReplaceGrantsForLicense(ctx context.Context, licenseID uuid.UUID, packageIDs []uuid.UUID, actions []string) error
	HasGrant(ctx context.Context, licenseID, packageID uuid.UUID, action string) (bool, error)

	// static admins (manifest-managed break-glass logins)
	ListStaticAdmins(ctx context.Context) ([]StaticAdmin, error)
	GetStaticAdminByEmail(ctx context.Context, email string) (*StaticAdmin, error)
	UpsertStaticAdmin(ctx context.Context, sa *StaticAdmin) error
	DeleteStaticAdmin(ctx context.Context, id uuid.UUID) error

	// Branding (runtime white-label overrides). Singleton row; Get always
	// returns the row (never ErrNotFound), Set updates it in place.
	GetBranding(ctx context.Context) (*Branding, error)
	SetBranding(ctx context.Context, b *Branding) error

	// audit
	InsertAuditEvent(e audit.AuditEvent) error
	ListAuditEvents(ctx context.Context, limit int, cursor *time.Time) ([]audit.AuditEvent, error)

	// lifecycle
	Close()
}
