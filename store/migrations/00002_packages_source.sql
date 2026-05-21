-- +goose Up
-- +goose StatementBegin

-- packages: add the source discriminator and GitHub-release config columns.
-- source defaults to 'oci' so every existing row stays valid; the new check
-- constraint accepts only the two supported values.
ALTER TABLE packages
    ADD COLUMN source TEXT NOT NULL DEFAULT 'oci'
        CHECK (source IN ('oci', 'github-release')),
    ADD COLUMN github_repo TEXT NOT NULL DEFAULT '',
    ADD COLUMN release_pattern TEXT NOT NULL DEFAULT '',
    ADD COLUMN asset_pattern TEXT NOT NULL DEFAULT '';

-- upstream_credentials.kind: expand the legal set to include 'github-api'.
-- The migration is forward-compatible — pre-existing rows have kind='ghcr'
-- and need no rewrite. We drop-and-recreate the check because the original
-- migration only stated NOT NULL without a CHECK; this codifies the allowed
-- values going forward.
ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;

ALTER TABLE packages
    DROP COLUMN IF EXISTS asset_pattern,
    DROP COLUMN IF EXISTS release_pattern,
    DROP COLUMN IF EXISTS github_repo,
    DROP COLUMN IF EXISTS source;

-- +goose StatementEnd
