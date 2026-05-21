-- +goose Up
-- +goose StatementBegin

-- Multi-registry upstream-credential support, Phase 4.
--
-- Adds 'gitlab-api' — a credential Kind for the GitLab Releases REST API.
-- Mirrors 'github-api' for the github-release source. Required for any
-- package whose source is 'gitlab-release'.
--
-- gitlab-api uses BaseURL so self-hosted GitLab instances are supported
-- alongside SaaS gitlab.com.
ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api', 'oci-basic',
                        'dockerhub', 'quay', 'gitlab',
                        'ecr', 'gar', 'acr-aad',
                        'gitlab-api'));

-- packages.source: expand to allow 'gitlab-release'.
ALTER TABLE packages
    DROP CONSTRAINT IF EXISTS packages_source_check;
ALTER TABLE packages
    ADD CONSTRAINT packages_source_check
        CHECK (source IN ('oci', 'github-release', 'gitlab-release'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE packages
    DROP CONSTRAINT IF EXISTS packages_source_check;
ALTER TABLE packages
    ADD CONSTRAINT packages_source_check
        CHECK (source IN ('oci', 'github-release'));

ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api', 'oci-basic',
                        'dockerhub', 'quay', 'gitlab',
                        'ecr', 'gar', 'acr-aad'));

-- +goose StatementEnd
