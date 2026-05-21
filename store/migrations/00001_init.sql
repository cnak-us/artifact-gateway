-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE oidc_providers (
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

CREATE TABLE users (
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

CREATE TABLE upstream_credentials (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name               TEXT NOT NULL UNIQUE,
    kind               TEXT NOT NULL,
    username           TEXT NOT NULL,
    pat_enc            BYTEA NOT NULL,
    pat_fingerprint    TEXT NOT NULL,
    last_used_at       TIMESTAMPTZ NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE packages (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                     TEXT NOT NULL UNIQUE,
    path                     TEXT NOT NULL UNIQUE,
    upstream_repo            TEXT NOT NULL,
    upstream_credential_id   UUID NOT NULL REFERENCES upstream_credentials(id) ON DELETE RESTRICT,
    kind                     TEXT NOT NULL CHECK (kind IN ('container','helm','binary')),
    display_name             TEXT NOT NULL DEFAULT '',
    description              TEXT NOT NULL DEFAULT '',
    release_notes_url        TEXT NOT NULL DEFAULT '',
    install_instructions_md  TEXT NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX packages_slug_idx ON packages (slug);
CREATE INDEX packages_path_idx ON packages (path);

CREATE TABLE licenses (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    license_id    TEXT NOT NULL UNIQUE,
    customer      TEXT NOT NULL DEFAULT '',
    organization  TEXT NOT NULL DEFAULT '',
    tier          TEXT NOT NULL DEFAULT '',
    expires_at    TIMESTAMPTZ NULL,
    lic_blob      TEXT NOT NULL,
    revoked_at    TIMESTAMPTZ NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX licenses_active_expires_idx
    ON licenses (expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE customer_tokens (
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

CREATE INDEX customer_tokens_token_id_idx  ON customer_tokens (token_id);
CREATE INDEX customer_tokens_license_id_idx ON customer_tokens (license_id);

CREATE TABLE package_grants (
    license_id  UUID NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    package_id  UUID NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    actions     TEXT[] NOT NULL DEFAULT ARRAY['pull'],
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (license_id, package_id)
);

CREATE TABLE audit_events (
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

CREATE INDEX audit_events_timestamp_idx ON audit_events (timestamp DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS package_grants;
DROP TABLE IF EXISTS customer_tokens;
DROP TABLE IF EXISTS licenses;
DROP TABLE IF EXISTS packages;
DROP TABLE IF EXISTS upstream_credentials;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS oidc_providers;
-- +goose StatementEnd
