-- +goose Up
-- +goose StatementBegin

-- Multi-registry upstream-credential support, Phase 1.
--
-- Adds three optional columns to upstream_credentials so non-'ghcr' kinds can
-- carry their own host and TLS trust. Existing 'ghcr' rows keep working
-- because all three columns default to empty/false.
--
--   base_url                    — required for 'oci-basic'; empty for 'ghcr'.
--   ca_bundle_pem               — optional PEM cert chain for internal CAs.
--   insecure_skip_tls_verify    — lab/dev escape hatch.
--
-- Also expands the kind CHECK to include 'oci-basic'. Future bucket-B/C kinds
-- (dockerhub, quay, gitlab, ecr, gar, acr-aad) will land in later migrations.
ALTER TABLE upstream_credentials
    ADD COLUMN IF NOT EXISTS base_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ca_bundle_pem TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS insecure_skip_tls_verify BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api', 'oci-basic'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api'));

ALTER TABLE upstream_credentials
    DROP COLUMN IF EXISTS insecure_skip_tls_verify,
    DROP COLUMN IF EXISTS ca_bundle_pem,
    DROP COLUMN IF EXISTS base_url;

-- +goose StatementEnd
