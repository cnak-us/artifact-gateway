-- +goose Up
-- +goose StatementBegin

-- Multi-registry upstream-credential support, Phase 2 (bearer-exchange).
--
-- Adds the bucket-B Kinds — registries that use the Docker token-exchange
-- handshake (`WWW-Authenticate: Bearer realm=…,service=…,scope=…`).
--
--   dockerhub  Docker Hub. Host is pinned (registry-1.docker.io).
--   quay       Quay.io (and self-hosted Quay). BaseURL is optional;
--              defaults to https://quay.io.
--   gitlab     GitLab Container Registry (SaaS + self-hosted). BaseURL
--              required (registry.gitlab.com or registry.<self-hosted>).
--
-- Static credential shape is unchanged — username + PAT/robot-token/deploy-
-- token; the BearerExchangeAuthenticator mints scope-pinned JWTs on demand.
ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api', 'oci-basic',
                        'dockerhub', 'quay', 'gitlab'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api', 'oci-basic'));

-- +goose StatementEnd
