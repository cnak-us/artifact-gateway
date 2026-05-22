-- artifact-gateway schema for a fresh Postgres database.
-- Applied once on startup against an empty DB; idempotent via IF NOT EXISTS.

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE IF NOT EXISTS oidc_providers (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name               TEXT NOT NULL UNIQUE,
    issuer_url         TEXT NOT NULL,
    client_id          TEXT NOT NULL,
    client_secret_enc  BYTEA NOT NULL,
    scopes             TEXT[] NOT NULL DEFAULT '{}',
    enabled            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email              CITEXT NOT NULL UNIQUE,
    password_hash      TEXT NOT NULL DEFAULT '',
    oidc_subject       TEXT NOT NULL DEFAULT '',
    oidc_provider_id   UUID NULL REFERENCES oidc_providers(id) ON DELETE SET NULL,
    role               TEXT NOT NULL DEFAULT 'admin',
    disabled_at        TIMESTAMPTZ NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS upstream_credentials (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                     TEXT NOT NULL UNIQUE,
    kind                     TEXT NOT NULL
        CHECK (kind IN ('ghcr', 'github-api', 'oci-basic',
                        'dockerhub', 'quay', 'gitlab',
                        'ecr', 'gar', 'acr-aad',
                        'gitlab-api')),
    username                 TEXT NOT NULL,
    pat_enc                  BYTEA,
    pat_fingerprint          TEXT,
    base_url                 TEXT NOT NULL DEFAULT '',
    ca_bundle_pem            TEXT NOT NULL DEFAULT '',
    insecure_skip_tls_verify BOOLEAN NOT NULL DEFAULT FALSE,
    issuer_kind              TEXT NOT NULL DEFAULT '',
    issuer_secret_enc        BYTEA,
    issuer_config            JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_used_at             TIMESTAMPTZ NULL,
    -- source tags the row owner: '' (legacy / admin-UI-created) or 'manifest'
    -- (reconciler-owned). Prune passes only delete 'manifest' rows so an
    -- admin's break-glass credential isn't wiped by an unrelated apply.
    source                   TEXT NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotent upgrade for pre-source DBs.
ALTER TABLE upstream_credentials
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS packages (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                     TEXT NOT NULL UNIQUE,
    path                     TEXT NOT NULL UNIQUE,
    upstream_repo            TEXT NOT NULL,
    upstream_credential_id   UUID NOT NULL REFERENCES upstream_credentials(id) ON DELETE RESTRICT,
    -- kind is a free-text display label only; no Go code switches on it for behavior.
    -- Common values: container, helm, binary, cli, zip, archive, image, chart.
    kind                     TEXT NOT NULL,
    display_name             TEXT NOT NULL DEFAULT '',
    description              TEXT NOT NULL DEFAULT '',
    release_notes_url        TEXT NOT NULL DEFAULT '',
    install_instructions_md  TEXT NOT NULL DEFAULT '',
    source                   TEXT NOT NULL DEFAULT 'oci'
        CHECK (source IN ('oci', 'github-release', 'gitlab-release')),
    github_repo              TEXT NOT NULL DEFAULT '',
    release_pattern          TEXT NOT NULL DEFAULT '',
    asset_pattern            TEXT NOT NULL DEFAULT '',
    -- managed_by tags the row owner: '' (legacy / admin-UI-created) or 'manifest'
    -- (reconciler-owned). Named distinctly from the pre-existing `source` column
    -- (which discriminates oci/github-release/gitlab-release) to avoid overload.
    managed_by               TEXT NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS packages_slug_idx ON packages (slug);
CREATE INDEX IF NOT EXISTS packages_path_idx ON packages (path);

-- Idempotent upgrade for pre-managed_by DBs.
ALTER TABLE packages
    ADD COLUMN IF NOT EXISTS managed_by TEXT NOT NULL DEFAULT '';

-- package_containers holds the per-container rows under a multi-container
-- package. Legacy single-container packages (which set packages.upstream_repo
-- and have zero rows here) continue to work via the fallback "the package IS
-- the container" semantic. The CHECK on alias blocks accidental nested paths
-- because the customer-facing URL is dl.cnak.us/<package.path>/<alias>.
CREATE TABLE IF NOT EXISTS package_containers (
    package_id    UUID NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    alias         TEXT NOT NULL CHECK (alias <> '' AND alias !~ '/'),
    upstream_repo TEXT NOT NULL,
    display_name  TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (package_id, alias)
);

CREATE INDEX IF NOT EXISTS package_containers_pkg_idx ON package_containers (package_id);
CREATE INDEX IF NOT EXISTS package_containers_source_idx ON package_containers (source);

CREATE TABLE IF NOT EXISTS licenses (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    license_id    TEXT NOT NULL UNIQUE,
    customer      TEXT NOT NULL DEFAULT '',
    organization  TEXT NOT NULL DEFAULT '',
    tier          TEXT NOT NULL DEFAULT '',
    expires_at    TIMESTAMPTZ NULL,
    lic_blob      TEXT NOT NULL,
    revoked_at    TIMESTAMPTZ NULL,
    -- source tags the row owner: '' (legacy / admin-UI-created) or 'manifest'
    -- (reconciler-owned). Prune passes only delete 'manifest' rows.
    source        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS licenses_active_expires_idx
    ON licenses (expires_at)
    WHERE revoked_at IS NULL;

-- Idempotent upgrade for pre-source DBs.
ALTER TABLE licenses
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS customer_tokens (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_id      TEXT NOT NULL UNIQUE,
    secret_hash   TEXT NOT NULL,
    license_id    UUID NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    description   TEXT NOT NULL DEFAULT '',
    expires_at    TIMESTAMPTZ NULL,
    revoked_at    TIMESTAMPTZ NULL,
    last_used_at  TIMESTAMPTZ NULL,
    created_by    UUID NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS customer_tokens_token_id_idx   ON customer_tokens (token_id);
CREATE INDEX IF NOT EXISTS customer_tokens_license_id_idx ON customer_tokens (license_id);

CREATE TABLE IF NOT EXISTS package_grants (
    license_id  UUID NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    package_id  UUID NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    actions     TEXT[] NOT NULL DEFAULT ARRAY['pull'],
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (license_id, package_id)
);

CREATE TABLE IF NOT EXISTS audit_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id         TEXT NOT NULL DEFAULT '',
    username        TEXT NOT NULL DEFAULT '',
    action          TEXT NOT NULL DEFAULT '',
    resource_type   TEXT NOT NULL DEFAULT '',
    resource_id     TEXT NOT NULL DEFAULT '',
    resource_name   TEXT NOT NULL DEFAULT '',
    details         TEXT NOT NULL DEFAULT '',
    ip_address      TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT '',
    error_message   TEXT NOT NULL DEFAULT '',
    source          TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_events_timestamp_idx ON audit_events (timestamp DESC);

CREATE TABLE IF NOT EXISTS static_admins (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         CITEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'manifest',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS static_admins_source_idx ON static_admins (source);

-- root_keys holds Ed25519 signing keys. private_key_enc NULL = verify-only.
-- The partial unique index enforces at most one active signing key at a time.
CREATE TABLE IF NOT EXISTS root_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    public_key      BYTEA NOT NULL,
    private_key_enc BYTEA NULL,
    fingerprint     TEXT NOT NULL UNIQUE,
    active          BOOLEAN NOT NULL DEFAULT FALSE,
    imported_from   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS root_keys_one_active_idx ON root_keys (active) WHERE active = TRUE;

CREATE TABLE IF NOT EXISTS license_contacts (
    license_id  UUID NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    email       CITEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    -- source tags the row owner: '' (legacy / admin-UI-created) or 'manifest'
    -- (reconciler-owned). Prune passes only delete 'manifest' rows.
    source      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (license_id, email)
);

CREATE INDEX IF NOT EXISTS license_contacts_email_idx ON license_contacts (email);

-- Idempotent upgrade for pre-source DBs.
ALTER TABLE license_contacts
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS license_contacts_source_idx ON license_contacts (source);

-- branding is a singleton (id=1). Empty/NULL fields fall through to the
-- compiled-in CNAK preset in the UI loader.
CREATE TABLE IF NOT EXISTS branding (
    id                   SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    product_name         TEXT,
    vendor               TEXT,
    vendor_short         TEXT,
    footer_tagline       TEXT,
    embedded_tagline     TEXT,
    catalog_hero_eyebrow TEXT,
    html_title           TEXT,
    meta_description     TEXT,
    accent_light_main    TEXT,
    accent_light_text    TEXT,
    accent_dark_main     TEXT,
    accent_dark_text     TEXT,
    logo_svg             TEXT,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by           TEXT
);

INSERT INTO branding (id) VALUES (1)
ON CONFLICT (id) DO NOTHING;
