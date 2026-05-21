-- +goose Up
-- +goose StatementBegin

-- license_contacts is the email allowlist for federated catalog login.
-- An email here is permitted to OIDC-sign-in to the catalog and inherit
-- the linked license's entitlements (packages, downloads, .lic blob,
-- customer-token minting). One license has 0..N contacts. The same email
-- MAY appear on multiple licenses (rare — e.g. a consultant) — v1 picks
-- the oldest matching license; v2 will offer a switcher.
CREATE TABLE license_contacts (
    license_id  UUID NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    email       CITEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (license_id, email)
);

CREATE INDEX license_contacts_email_idx ON license_contacts (email);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS license_contacts;
-- +goose StatementEnd
