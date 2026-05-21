-- +goose Up
-- +goose StatementBegin

-- Multi-registry upstream-credential support, Phase 3 (cloud issuer-mint).
--
-- Adds the bucket-C Kinds — registries that require a short-lived registry
-- token minted from a stored issuer credential (IAM keys, SA JSON, AAD SP):
--
--   ecr      AWS ECR. issuer_secret_enc holds JSON {accessKeyId, secretAccessKey}.
--            issuer_config holds {"region": "us-east-1", "accountId": "..."}.
--   gar      Google Artifact Registry / GCR. issuer_secret_enc holds the raw
--            service-account JSON key. issuer_config may carry {"audience": "..."}.
--   acr-aad  Azure ACR via AAD. issuer_secret_enc holds JSON {clientId, clientSecret}.
--            issuer_config holds {"tenantId": "...", "registry": "<name>.azurecr.io"}.
--
-- New columns on upstream_credentials:
--   issuer_kind       short string mirroring Kind (ecr|gar|acr-aad), empty for
--                     non-bucket-C rows. Kept separate so future bucket-C kinds
--                     can share the same column.
--   issuer_secret_enc KEK-wrapped JSON. NULL for non-issuer rows.
--   issuer_config     JSONB of non-secret per-cloud config. Defaults to '{}'.
ALTER TABLE upstream_credentials
    ADD COLUMN IF NOT EXISTS issuer_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS issuer_secret_enc BYTEA,
    ADD COLUMN IF NOT EXISTS issuer_config JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Bucket-C rows have no static PAT — the cloud SDK mints registry tokens
-- from issuer_secret_enc. Drop the NOT NULL so an empty pat_enc is legal.
ALTER TABLE upstream_credentials
    ALTER COLUMN pat_enc DROP NOT NULL,
    ALTER COLUMN pat_fingerprint DROP NOT NULL;

ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api', 'oci-basic',
                        'dockerhub', 'quay', 'gitlab',
                        'ecr', 'gar', 'acr-aad'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api', 'oci-basic',
                        'dockerhub', 'quay', 'gitlab'));

ALTER TABLE upstream_credentials
    DROP COLUMN IF EXISTS issuer_config,
    DROP COLUMN IF EXISTS issuer_secret_enc,
    DROP COLUMN IF EXISTS issuer_kind;

-- Best-effort: restore NOT NULL. Will fail if any issuer-only rows exist.
ALTER TABLE upstream_credentials
    ALTER COLUMN pat_enc SET NOT NULL,
    ALTER COLUMN pat_fingerprint SET NOT NULL;

-- +goose StatementEnd
