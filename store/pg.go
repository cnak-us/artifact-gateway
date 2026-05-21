// Package store — Postgres implementation of DataStore.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PG is the Postgres-backed DataStore.
type PG struct {
	pool *pgxpool.Pool
}

// New opens a pgxpool against dsn and pings to verify connectivity.
func New(ctx context.Context, dsn string) (*PG, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &PG{pool: pool}, nil
}

// Close releases the underlying connection pool.
func (p *PG) Close() {
	if p.pool != nil {
		p.pool.Close()
	}
}

// Pool exposes the underlying pgxpool. Useful for adapters (e.g. goose) that
// need a database/sql handle via stdlib.OpenDBFromPool.
func (p *PG) Pool() *pgxpool.Pool { return p.pool }

// SQLDB returns a *sql.DB backed by the same pool, suitable for goose.
// The caller is responsible for closing the returned *sql.DB.
func (p *PG) SQLDB() *sql.DB {
	return stdlib.OpenDBFromPool(p.pool)
}

// RunMigrations applies all embedded goose migrations against db.
// Pass the result of PG.SQLDB() (or any *sql.DB) — goose needs database/sql.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// ---------- helpers ----------

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---------- users ----------

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.OIDCSubject, &u.OIDCProviderID,
		&u.Role, &u.DisabledAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}

const userCols = `id, email, password_hash, oidc_subject, oidc_provider_id, role, disabled_at, created_at, updated_at`

func (p *PG) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, email)
	return scanUser(row)
}

func (p *PG) GetUserByOIDC(ctx context.Context, providerID uuid.UUID, subject string) (*User, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE oidc_provider_id = $1 AND oidc_subject = $2`,
		providerID, subject)
	return scanUser(row)
}

func (p *PG) InsertUser(ctx context.Context, u *User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now
	_, err := p.pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, oidc_subject, oidc_provider_id, role, disabled_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		u.ID, u.Email, u.PasswordHash, u.OIDCSubject, u.OIDCProviderID,
		u.Role, u.DisabledAt, u.CreatedAt, u.UpdatedAt,
	)
	return err
}

func (p *PG) UpdateUser(ctx context.Context, u *User) error {
	u.UpdatedAt = time.Now().UTC()
	tag, err := p.pool.Exec(ctx,
		`UPDATE users SET email=$2, password_hash=$3, oidc_subject=$4, oidc_provider_id=$5,
		 role=$6, disabled_at=$7, updated_at=$8 WHERE id=$1`,
		u.ID, u.Email, u.PasswordHash, u.OIDCSubject, u.OIDCProviderID,
		u.Role, u.DisabledAt, u.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PG) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.Email, &u.PasswordHash, &u.OIDCSubject, &u.OIDCProviderID,
			&u.Role, &u.DisabledAt, &u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (p *PG) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ---------- oidc providers ----------

const oidcCols = `id, name, issuer_url, client_id, client_secret_enc, scopes, enabled, created_at, updated_at`

func scanOIDC(row pgx.Row) (*OIDCProvider, error) {
	var o OIDCProvider
	err := row.Scan(
		&o.ID, &o.Name, &o.IssuerURL, &o.ClientID, &o.ClientSecretEnc,
		&o.Scopes, &o.Enabled, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &o, nil
}

func (p *PG) ListOIDCProviders(ctx context.Context) ([]OIDCProvider, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+oidcCols+` FROM oidc_providers ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OIDCProvider
	for rows.Next() {
		var o OIDCProvider
		if err := rows.Scan(
			&o.ID, &o.Name, &o.IssuerURL, &o.ClientID, &o.ClientSecretEnc,
			&o.Scopes, &o.Enabled, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (p *PG) GetOIDCProvider(ctx context.Context, id uuid.UUID) (*OIDCProvider, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+oidcCols+` FROM oidc_providers WHERE id=$1`, id)
	return scanOIDC(row)
}

func (p *PG) GetOIDCProviderByName(ctx context.Context, name string) (*OIDCProvider, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+oidcCols+` FROM oidc_providers WHERE name=$1`, name)
	return scanOIDC(row)
}

func (p *PG) InsertOIDCProvider(ctx context.Context, o *OIDCProvider) error {
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	now := time.Now().UTC()
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now
	}
	o.UpdatedAt = now
	_, err := p.pool.Exec(ctx,
		`INSERT INTO oidc_providers (id, name, issuer_url, client_id, client_secret_enc, scopes, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		o.ID, o.Name, o.IssuerURL, o.ClientID, o.ClientSecretEnc, o.Scopes, o.Enabled, o.CreatedAt, o.UpdatedAt,
	)
	return err
}

func (p *PG) DeleteOIDCProvider(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM oidc_providers WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- upstream credentials ----------

const upstreamCols = `id, name, kind, username, pat_enc, pat_fingerprint, base_url, ca_bundle_pem, insecure_skip_tls_verify, issuer_kind, issuer_secret_enc, issuer_config, last_used_at, created_at, updated_at`

func scanUpstream(row pgx.Row) (*UpstreamCredential, error) {
	var c UpstreamCredential
	err := row.Scan(
		&c.ID, &c.Name, &c.Kind, &c.Username, &c.PATEnc,
		&c.PATFingerprint, &c.BaseURL, &c.CABundlePEM, &c.InsecureSkipTLSVerify,
		&c.IssuerKind, &c.IssuerSecretEnc, &c.IssuerConfigJSON,
		&c.LastUsedAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

func (p *PG) ListUpstreamCredentials(ctx context.Context) ([]UpstreamCredential, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+upstreamCols+` FROM upstream_credentials ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpstreamCredential
	for rows.Next() {
		var c UpstreamCredential
		if err := rows.Scan(
			&c.ID, &c.Name, &c.Kind, &c.Username, &c.PATEnc,
			&c.PATFingerprint, &c.BaseURL, &c.CABundlePEM, &c.InsecureSkipTLSVerify,
			&c.IssuerKind, &c.IssuerSecretEnc, &c.IssuerConfigJSON,
			&c.LastUsedAt, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (p *PG) GetUpstreamCredential(ctx context.Context, id uuid.UUID) (*UpstreamCredential, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+upstreamCols+` FROM upstream_credentials WHERE id=$1`, id)
	return scanUpstream(row)
}

func (p *PG) InsertUpstreamCredential(ctx context.Context, c *UpstreamCredential) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	cfg := c.IssuerConfigJSON
	if len(cfg) == 0 {
		cfg = []byte(`{}`)
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO upstream_credentials (id, name, kind, username, pat_enc, pat_fingerprint, base_url, ca_bundle_pem, insecure_skip_tls_verify, issuer_kind, issuer_secret_enc, issuer_config, last_used_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		c.ID, c.Name, c.Kind, c.Username, c.PATEnc, c.PATFingerprint,
		c.BaseURL, c.CABundlePEM, c.InsecureSkipTLSVerify,
		c.IssuerKind, c.IssuerSecretEnc, cfg,
		c.LastUsedAt, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (p *PG) DeleteUpstreamCredential(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM upstream_credentials WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PG) TouchUpstreamCredential(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	tag, err := p.pool.Exec(ctx,
		`UPDATE upstream_credentials SET last_used_at=$2, updated_at=$2 WHERE id=$1`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- packages ----------

const packageCols = `id, slug, path, upstream_repo, upstream_credential_id, kind, display_name, description, release_notes_url, install_instructions_md, source, github_repo, release_pattern, asset_pattern, created_at, updated_at`

func scanPackage(row pgx.Row) (*Package, error) {
	var pk Package
	err := row.Scan(
		&pk.ID, &pk.Slug, &pk.Path, &pk.UpstreamRepo, &pk.UpstreamCredentialID,
		&pk.Kind, &pk.DisplayName, &pk.Description, &pk.ReleaseNotesURL,
		&pk.InstallInstructionsMD, &pk.Source, &pk.GitHubRepo, &pk.ReleasePattern,
		&pk.AssetPattern, &pk.CreatedAt, &pk.UpdatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &pk, nil
}

func (p *PG) ListPackages(ctx context.Context) ([]Package, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+packageCols+` FROM packages ORDER BY slug ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Package
	for rows.Next() {
		var pk Package
		if err := rows.Scan(
			&pk.ID, &pk.Slug, &pk.Path, &pk.UpstreamRepo, &pk.UpstreamCredentialID,
			&pk.Kind, &pk.DisplayName, &pk.Description, &pk.ReleaseNotesURL,
			&pk.InstallInstructionsMD, &pk.Source, &pk.GitHubRepo, &pk.ReleasePattern,
			&pk.AssetPattern, &pk.CreatedAt, &pk.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}

func (p *PG) GetPackage(ctx context.Context, id uuid.UUID) (*Package, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+packageCols+` FROM packages WHERE id=$1`, id)
	return scanPackage(row)
}

func (p *PG) GetPackageByPath(ctx context.Context, path string) (*Package, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+packageCols+` FROM packages WHERE path=$1`, path)
	return scanPackage(row)
}

func (p *PG) GetPackageBySlug(ctx context.Context, slug string) (*Package, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+packageCols+` FROM packages WHERE slug=$1`, slug)
	return scanPackage(row)
}

func (p *PG) InsertPackage(ctx context.Context, pk *Package) error {
	if pk.ID == uuid.Nil {
		pk.ID = uuid.New()
	}
	if pk.Source == "" {
		pk.Source = "oci"
	}
	now := time.Now().UTC()
	if pk.CreatedAt.IsZero() {
		pk.CreatedAt = now
	}
	pk.UpdatedAt = now
	_, err := p.pool.Exec(ctx,
		`INSERT INTO packages (id, slug, path, upstream_repo, upstream_credential_id, kind, display_name, description, release_notes_url, install_instructions_md, source, github_repo, release_pattern, asset_pattern, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		pk.ID, pk.Slug, pk.Path, pk.UpstreamRepo, pk.UpstreamCredentialID,
		pk.Kind, pk.DisplayName, pk.Description, pk.ReleaseNotesURL,
		pk.InstallInstructionsMD, pk.Source, pk.GitHubRepo, pk.ReleasePattern,
		pk.AssetPattern, pk.CreatedAt, pk.UpdatedAt,
	)
	return err
}

func (p *PG) UpdatePackage(ctx context.Context, pk *Package) error {
	if pk.Source == "" {
		pk.Source = "oci"
	}
	pk.UpdatedAt = time.Now().UTC()
	tag, err := p.pool.Exec(ctx,
		`UPDATE packages SET slug=$2, path=$3, upstream_repo=$4, upstream_credential_id=$5,
		 kind=$6, display_name=$7, description=$8, release_notes_url=$9, install_instructions_md=$10,
		 source=$11, github_repo=$12, release_pattern=$13, asset_pattern=$14, updated_at=$15 WHERE id=$1`,
		pk.ID, pk.Slug, pk.Path, pk.UpstreamRepo, pk.UpstreamCredentialID,
		pk.Kind, pk.DisplayName, pk.Description, pk.ReleaseNotesURL,
		pk.InstallInstructionsMD, pk.Source, pk.GitHubRepo, pk.ReleasePattern,
		pk.AssetPattern, pk.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PG) DeletePackage(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM packages WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- licenses ----------

const licenseCols = `id, license_id, customer, organization, tier, expires_at, lic_blob, revoked_at, created_at, updated_at`

func scanLicense(row pgx.Row) (*License, error) {
	var l License
	err := row.Scan(
		&l.ID, &l.LicenseID, &l.Customer, &l.Organization, &l.Tier,
		&l.ExpiresAt, &l.LicBlob, &l.RevokedAt, &l.CreatedAt, &l.UpdatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &l, nil
}

func (p *PG) ListLicenses(ctx context.Context) ([]License, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+licenseCols+` FROM licenses ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []License
	for rows.Next() {
		var l License
		if err := rows.Scan(
			&l.ID, &l.LicenseID, &l.Customer, &l.Organization, &l.Tier,
			&l.ExpiresAt, &l.LicBlob, &l.RevokedAt, &l.CreatedAt, &l.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (p *PG) GetLicense(ctx context.Context, id uuid.UUID) (*License, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+licenseCols+` FROM licenses WHERE id=$1`, id)
	return scanLicense(row)
}

func (p *PG) GetLicenseByLicenseID(ctx context.Context, licID string) (*License, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+licenseCols+` FROM licenses WHERE license_id=$1`, licID)
	return scanLicense(row)
}

func (p *PG) InsertLicense(ctx context.Context, l *License) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	now := time.Now().UTC()
	if l.CreatedAt.IsZero() {
		l.CreatedAt = now
	}
	l.UpdatedAt = now
	_, err := p.pool.Exec(ctx,
		`INSERT INTO licenses (id, license_id, customer, organization, tier, expires_at, lic_blob, revoked_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		l.ID, l.LicenseID, l.Customer, l.Organization, l.Tier,
		l.ExpiresAt, l.LicBlob, l.RevokedAt, l.CreatedAt, l.UpdatedAt,
	)
	return err
}

func (p *PG) RevokeLicense(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	tag, err := p.pool.Exec(ctx,
		`UPDATE licenses SET revoked_at=$2, updated_at=$2 WHERE id=$1 AND revoked_at IS NULL`,
		id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PG) DeleteLicense(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM licenses WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- root keys ----------

const rootKeyCols = `id, name, public_key, private_key_enc, fingerprint, active, imported_from, created_at, updated_at`

func scanRootKey(row pgx.Row) (*RootKey, error) {
	var k RootKey
	err := row.Scan(
		&k.ID, &k.Name, &k.PublicKey, &k.PrivateKeyEnc, &k.Fingerprint,
		&k.Active, &k.ImportedFrom, &k.CreatedAt, &k.UpdatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &k, nil
}

func (p *PG) ListRootKeys(ctx context.Context) ([]RootKey, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+rootKeyCols+` FROM root_keys ORDER BY active DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RootKey
	for rows.Next() {
		var k RootKey
		if err := rows.Scan(
			&k.ID, &k.Name, &k.PublicKey, &k.PrivateKeyEnc, &k.Fingerprint,
			&k.Active, &k.ImportedFrom, &k.CreatedAt, &k.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (p *PG) GetRootKey(ctx context.Context, id uuid.UUID) (*RootKey, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+rootKeyCols+` FROM root_keys WHERE id=$1`, id)
	return scanRootKey(row)
}

func (p *PG) GetRootKeyByFingerprint(ctx context.Context, fp string) (*RootKey, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+rootKeyCols+` FROM root_keys WHERE fingerprint=$1`, fp)
	return scanRootKey(row)
}

func (p *PG) GetActiveSigningKey(ctx context.Context) (*RootKey, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT `+rootKeyCols+` FROM root_keys WHERE active = TRUE AND private_key_enc IS NOT NULL`)
	return scanRootKey(row)
}

func (p *PG) InsertRootKey(ctx context.Context, k *RootKey) error {
	if k.ID == uuid.Nil {
		k.ID = uuid.New()
	}
	now := time.Now().UTC()
	if k.CreatedAt.IsZero() {
		k.CreatedAt = now
	}
	k.UpdatedAt = now
	_, err := p.pool.Exec(ctx,
		`INSERT INTO root_keys (id, name, public_key, private_key_enc, fingerprint, active, imported_from, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		k.ID, k.Name, k.PublicKey, k.PrivateKeyEnc, k.Fingerprint,
		k.Active, k.ImportedFrom, k.CreatedAt, k.UpdatedAt,
	)
	return err
}

// SetActiveRootKey atomically clears active on every row then sets it on id.
// Refuses verify-only rows (no private key) so we never end up with an active
// key we can't sign with.
func (p *PG) SetActiveRootKey(ctx context.Context, id uuid.UUID) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var hasPriv bool
	err = tx.QueryRow(ctx,
		`SELECT private_key_enc IS NOT NULL FROM root_keys WHERE id=$1`, id,
	).Scan(&hasPriv)
	if err != nil {
		return mapErr(err)
	}
	if !hasPriv {
		return errors.New("cannot activate verify-only root key (no private key on file)")
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx,
		`UPDATE root_keys SET active=FALSE, updated_at=$1 WHERE active=TRUE`, now); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE root_keys SET active=TRUE, updated_at=$1 WHERE id=$2`, now, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// DeleteRootKey refuses to delete the active row. Verify-only and inactive
// signing rows can be removed; older .lic files signed by a deleted key will
// stop verifying — that's the operator's call to make.
func (p *PG) DeleteRootKey(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM root_keys WHERE id=$1 AND active=FALSE`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "active, refused" from "not found" with a follow-up read.
		var active bool
		if rerr := p.pool.QueryRow(ctx, `SELECT active FROM root_keys WHERE id=$1`, id).Scan(&active); rerr != nil {
			return ErrNotFound
		}
		return errors.New("cannot delete the active root key; activate a different key first")
	}
	return nil
}

// ---------- customer tokens ----------

const tokenCols = `id, token_id, secret_hash, license_id, description, expires_at, revoked_at, last_used_at, created_by, created_at, updated_at`

func scanToken(row pgx.Row) (*CustomerToken, error) {
	var t CustomerToken
	err := row.Scan(
		&t.ID, &t.TokenID, &t.SecretHash, &t.LicenseID, &t.Description,
		&t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedBy,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

func (p *PG) ListCustomerTokens(ctx context.Context, licenseID *uuid.UUID) ([]CustomerToken, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if licenseID != nil {
		rows, err = p.pool.Query(ctx,
			`SELECT `+tokenCols+` FROM customer_tokens WHERE license_id=$1 ORDER BY created_at DESC`,
			*licenseID)
	} else {
		rows, err = p.pool.Query(ctx,
			`SELECT `+tokenCols+` FROM customer_tokens ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomerToken
	for rows.Next() {
		var t CustomerToken
		if err := rows.Scan(
			&t.ID, &t.TokenID, &t.SecretHash, &t.LicenseID, &t.Description,
			&t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedBy,
			&t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *PG) GetCustomerToken(ctx context.Context, id uuid.UUID) (*CustomerToken, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+tokenCols+` FROM customer_tokens WHERE id=$1`, id)
	return scanToken(row)
}

func (p *PG) GetCustomerTokenByTokenID(ctx context.Context, tokenID string) (*CustomerToken, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+tokenCols+` FROM customer_tokens WHERE token_id=$1`, tokenID)
	return scanToken(row)
}

func (p *PG) InsertCustomerToken(ctx context.Context, t *CustomerToken) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	_, err := p.pool.Exec(ctx,
		`INSERT INTO customer_tokens (id, token_id, secret_hash, license_id, description, expires_at, revoked_at, last_used_at, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		t.ID, t.TokenID, t.SecretHash, t.LicenseID, t.Description,
		t.ExpiresAt, t.RevokedAt, t.LastUsedAt, t.CreatedBy,
		t.CreatedAt, t.UpdatedAt,
	)
	return err
}

func (p *PG) RevokeCustomerToken(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	tag, err := p.pool.Exec(ctx,
		`UPDATE customer_tokens SET revoked_at=$2, updated_at=$2 WHERE id=$1 AND revoked_at IS NULL`,
		id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PG) TouchCustomerToken(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	tag, err := p.pool.Exec(ctx,
		`UPDATE customer_tokens SET last_used_at=$2, updated_at=$2 WHERE id=$1`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PG) CountActiveCustomerTokens(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM customer_tokens
		 WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`).Scan(&n)
	return n, err
}

// ---------- license contacts ----------

const licenseContactCols = `license_id, email, name, created_at, updated_at`

func scanLicenseContact(row pgx.Row) (*LicenseContact, error) {
	var c LicenseContact
	err := row.Scan(&c.LicenseID, &c.Email, &c.Name, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

func (p *PG) ListContactsForLicense(ctx context.Context, licenseID uuid.UUID) ([]LicenseContact, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT `+licenseContactCols+` FROM license_contacts WHERE license_id=$1 ORDER BY created_at ASC`,
		licenseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LicenseContact
	for rows.Next() {
		var c LicenseContact
		if err := rows.Scan(&c.LicenseID, &c.Email, &c.Name, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (p *PG) AddContact(ctx context.Context, c *LicenseContact) error {
	c.Email = strings.ToLower(c.Email)
	_, err := p.pool.Exec(ctx,
		`INSERT INTO license_contacts (license_id, email, name)
		 VALUES ($1, LOWER($2), $3)
		 ON CONFLICT (license_id, email) DO UPDATE
		   SET name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE license_contacts.name END,
		       updated_at = now()`,
		c.LicenseID, c.Email, c.Name)
	return err
}

func (p *PG) RemoveContact(ctx context.Context, licenseID uuid.UUID, email string) error {
	email = strings.ToLower(email)
	_, err := p.pool.Exec(ctx,
		`DELETE FROM license_contacts WHERE license_id=$1 AND email=$2`, licenseID, email)
	return err
}

func (p *PG) FindLicensesByContactEmail(ctx context.Context, email string) ([]License, error) {
	email = strings.ToLower(email)
	rows, err := p.pool.Query(ctx,
		`SELECT l.id, l.license_id, l.customer, l.organization, l.tier,
		        l.expires_at, l.lic_blob, l.revoked_at, l.created_at, l.updated_at
		 FROM licenses l
		 INNER JOIN license_contacts c ON c.license_id = l.id
		 WHERE c.email = $1 AND l.revoked_at IS NULL
		 ORDER BY l.created_at ASC`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []License
	for rows.Next() {
		var l License
		if err := rows.Scan(
			&l.ID, &l.LicenseID, &l.Customer, &l.Organization, &l.Tier,
			&l.ExpiresAt, &l.LicBlob, &l.RevokedAt, &l.CreatedAt, &l.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---------- package grants ----------

func (p *PG) ListGrantsForLicense(ctx context.Context, licenseID uuid.UUID) ([]PackageGrant, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT license_id, package_id, actions FROM package_grants WHERE license_id=$1`, licenseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageGrant
	for rows.Next() {
		var g PackageGrant
		if err := rows.Scan(&g.LicenseID, &g.PackageID, &g.Actions); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (p *PG) GrantedPackagesForLicense(ctx context.Context, licenseID uuid.UUID) ([]Package, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT p.id, p.slug, p.path, p.upstream_repo, p.upstream_credential_id,
		        p.kind, p.display_name, p.description, p.release_notes_url,
		        p.install_instructions_md, p.source, p.github_repo, p.release_pattern,
		        p.asset_pattern, p.created_at, p.updated_at
		 FROM packages p
		 INNER JOIN package_grants g ON g.package_id = p.id
		 WHERE g.license_id = $1
		 ORDER BY p.slug ASC`, licenseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Package
	for rows.Next() {
		var pk Package
		if err := rows.Scan(
			&pk.ID, &pk.Slug, &pk.Path, &pk.UpstreamRepo, &pk.UpstreamCredentialID,
			&pk.Kind, &pk.DisplayName, &pk.Description, &pk.ReleaseNotesURL,
			&pk.InstallInstructionsMD, &pk.Source, &pk.GitHubRepo, &pk.ReleasePattern,
			&pk.AssetPattern, &pk.CreatedAt, &pk.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}

func (p *PG) ReplaceGrantsForLicense(ctx context.Context, licenseID uuid.UUID, packageIDs []uuid.UUID, actions []string) error {
	if actions == nil {
		actions = []string{"pull"}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM package_grants WHERE license_id=$1`, licenseID); err != nil {
		return err
	}
	for _, pid := range packageIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO package_grants (license_id, package_id, actions) VALUES ($1,$2,$3)`,
			licenseID, pid, actions); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *PG) HasGrant(ctx context.Context, licenseID, packageID uuid.UUID, action string) (bool, error) {
	var ok bool
	err := p.pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM package_grants
		   WHERE license_id=$1 AND package_id=$2 AND $3 = ANY(actions)
		 )`, licenseID, packageID, action).Scan(&ok)
	return ok, err
}

// ---------- static admins ----------

const staticAdminCols = `id, email, password_hash, source, created_at, updated_at`

func scanStaticAdmin(row pgx.Row) (*StaticAdmin, error) {
	var sa StaticAdmin
	err := row.Scan(&sa.ID, &sa.Email, &sa.PasswordHash, &sa.Source, &sa.CreatedAt, &sa.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &sa, nil
}

func (p *PG) ListStaticAdmins(ctx context.Context) ([]StaticAdmin, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+staticAdminCols+` FROM static_admins ORDER BY email ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaticAdmin
	for rows.Next() {
		var sa StaticAdmin
		if err := rows.Scan(&sa.ID, &sa.Email, &sa.PasswordHash, &sa.Source, &sa.CreatedAt, &sa.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sa)
	}
	return out, rows.Err()
}

func (p *PG) GetStaticAdminByEmail(ctx context.Context, email string) (*StaticAdmin, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+staticAdminCols+` FROM static_admins WHERE email=$1`, email)
	return scanStaticAdmin(row)
}

// UpsertStaticAdmin inserts a new row or replaces password_hash/source on
// conflict by email. ID is honored on insert; ignored when an existing row
// is updated (we don't change a row's UUID).
func (p *PG) UpsertStaticAdmin(ctx context.Context, sa *StaticAdmin) error {
	if sa.ID == uuid.Nil {
		sa.ID = uuid.New()
	}
	if sa.Source == "" {
		sa.Source = "manifest"
	}
	now := time.Now().UTC()
	if sa.CreatedAt.IsZero() {
		sa.CreatedAt = now
	}
	sa.UpdatedAt = now
	_, err := p.pool.Exec(ctx,
		`INSERT INTO static_admins (id, email, password_hash, source, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (email) DO UPDATE
		   SET password_hash = EXCLUDED.password_hash,
		       source        = EXCLUDED.source,
		       updated_at    = EXCLUDED.updated_at`,
		sa.ID, sa.Email, sa.PasswordHash, sa.Source, sa.CreatedAt, sa.UpdatedAt)
	return err
}

func (p *PG) DeleteStaticAdmin(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM static_admins WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- branding ----------

// brandingCols mirrors the column order used by both scanBranding and the
// UPDATE statement in SetBranding. Keep these three in lock-step.
const brandingCols = `product_name, vendor, vendor_short, footer_tagline, embedded_tagline,
	catalog_hero_eyebrow, html_title, meta_description,
	accent_light_main, accent_light_text, accent_dark_main, accent_dark_text,
	logo_svg, updated_at, updated_by`

// nullableString unwraps a sql.NullString to an empty string when NULL — the
// JSON layer treats "" as "use preset default", so we don't surface NULL to
// callers.
func nullableString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// nsFrom is the inverse: empty string -> NULL, non-empty -> Valid.
func nsFrom(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// GetBranding reads the singleton row. The migration seeds id=1, but a missing
// row is treated as "no overrides set" rather than an error — the UI falls
// back to the compiled-in preset for every field.
func (p *PG) GetBranding(ctx context.Context) (*Branding, error) {
	var (
		b                                                                                Branding
		productName, vendor, vendorShort, footerTagline, embeddedTagline                 sql.NullString
		catalogHeroEyebrow, htmlTitle, metaDescription                                   sql.NullString
		accentLightMain, accentLightText, accentDarkMain, accentDarkText, logoSVG, upBy sql.NullString
	)
	err := p.pool.QueryRow(ctx, `SELECT `+brandingCols+` FROM branding WHERE id = 1`).Scan(
		&productName, &vendor, &vendorShort, &footerTagline, &embeddedTagline,
		&catalogHeroEyebrow, &htmlTitle, &metaDescription,
		&accentLightMain, &accentLightText, &accentDarkMain, &accentDarkText,
		&logoSVG, &b.UpdatedAt, &upBy,
	)
	if err != nil {
		if errors.Is(mapErr(err), ErrNotFound) {
			return &Branding{}, nil
		}
		return nil, err
	}
	b.ProductName = nullableString(productName)
	b.Vendor = nullableString(vendor)
	b.VendorShort = nullableString(vendorShort)
	b.FooterTagline = nullableString(footerTagline)
	b.EmbeddedTagline = nullableString(embeddedTagline)
	b.CatalogHeroEyebrow = nullableString(catalogHeroEyebrow)
	b.HTMLTitle = nullableString(htmlTitle)
	b.MetaDescription = nullableString(metaDescription)
	b.AccentLightMain = nullableString(accentLightMain)
	b.AccentLightText = nullableString(accentLightText)
	b.AccentDarkMain = nullableString(accentDarkMain)
	b.AccentDarkText = nullableString(accentDarkText)
	b.LogoSVG = nullableString(logoSVG)
	b.UpdatedBy = nullableString(upBy)
	return &b, nil
}

// SetBranding upserts the singleton row. Empty fields are written as NULL so a
// future schema change that distinguishes "" from NULL keeps working. ON
// CONFLICT keeps the call safe even if the seed INSERT was skipped or the row
// was truncated by hand.
func (p *PG) SetBranding(ctx context.Context, b *Branding) error {
	now := time.Now().UTC()
	b.UpdatedAt = now
	_, err := p.pool.Exec(ctx,
		`INSERT INTO branding (
		   id, product_name, vendor, vendor_short, footer_tagline,
		   embedded_tagline, catalog_hero_eyebrow, html_title, meta_description,
		   accent_light_main, accent_light_text, accent_dark_main, accent_dark_text,
		   logo_svg, updated_at, updated_by
		 ) VALUES (
		   1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		 )
		 ON CONFLICT (id) DO UPDATE SET
		   product_name = EXCLUDED.product_name, vendor = EXCLUDED.vendor,
		   vendor_short = EXCLUDED.vendor_short, footer_tagline = EXCLUDED.footer_tagline,
		   embedded_tagline = EXCLUDED.embedded_tagline,
		   catalog_hero_eyebrow = EXCLUDED.catalog_hero_eyebrow,
		   html_title = EXCLUDED.html_title, meta_description = EXCLUDED.meta_description,
		   accent_light_main = EXCLUDED.accent_light_main,
		   accent_light_text = EXCLUDED.accent_light_text,
		   accent_dark_main = EXCLUDED.accent_dark_main,
		   accent_dark_text = EXCLUDED.accent_dark_text,
		   logo_svg = EXCLUDED.logo_svg,
		   updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
		nsFrom(b.ProductName), nsFrom(b.Vendor), nsFrom(b.VendorShort), nsFrom(b.FooterTagline),
		nsFrom(b.EmbeddedTagline), nsFrom(b.CatalogHeroEyebrow), nsFrom(b.HTMLTitle),
		nsFrom(b.MetaDescription), nsFrom(b.AccentLightMain), nsFrom(b.AccentLightText),
		nsFrom(b.AccentDarkMain), nsFrom(b.AccentDarkText), nsFrom(b.LogoSVG),
		now, nsFrom(b.UpdatedBy),
	)
	return err
}

// ---------- audit ----------

func (p *PG) InsertAuditEvent(e audit.AuditEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := parseOrNewUUID(e.ID)
	if err != nil {
		return fmt.Errorf("audit id: %w", err)
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	_, err = p.pool.Exec(ctx,
		`INSERT INTO audit_events
		 (id, timestamp, user_id, username, action, resource_type, resource_id, resource_name,
		  details, ip_address, status, error_message, source)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		id, e.Timestamp, e.UserID, e.Username, e.Action, e.ResourceType,
		e.ResourceID, e.ResourceName, e.Details, e.IPAddress, e.Status,
		e.ErrorMessage, e.Source,
	)
	return err
}

func (p *PG) ListAuditEvents(ctx context.Context, limit int, cursor *time.Time) ([]audit.AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	var (
		rows pgx.Rows
		err  error
	)
	if cursor != nil {
		rows, err = p.pool.Query(ctx,
			`SELECT id, timestamp, user_id, username, action, resource_type, resource_id, resource_name,
			        details, ip_address, status, error_message, source
			 FROM audit_events
			 WHERE timestamp < $1
			 ORDER BY timestamp DESC
			 LIMIT $2`, *cursor, limit)
	} else {
		rows, err = p.pool.Query(ctx,
			`SELECT id, timestamp, user_id, username, action, resource_type, resource_id, resource_name,
			        details, ip_address, status, error_message, source
			 FROM audit_events
			 ORDER BY timestamp DESC
			 LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []audit.AuditEvent
	for rows.Next() {
		var (
			e  audit.AuditEvent
			id uuid.UUID
		)
		if err := rows.Scan(
			&id, &e.Timestamp, &e.UserID, &e.Username, &e.Action, &e.ResourceType,
			&e.ResourceID, &e.ResourceName, &e.Details, &e.IPAddress, &e.Status,
			&e.ErrorMessage, &e.Source,
		); err != nil {
			return nil, err
		}
		e.ID = id.String()
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------- internal ----------

func parseOrNewUUID(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.New(), nil
	}
	return uuid.Parse(s)
}

// Compile-time assertions.
var _ DataStore = (*PG)(nil)
