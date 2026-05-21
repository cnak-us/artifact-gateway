-- +goose Up
-- +goose StatementBegin

-- static_admins is the per-row backing for declarative break-glass admin
-- entries. Distinct from the users table because (a) it has no OIDC fields
-- and (b) the apply tool needs to own its rows for prune semantics — the
-- `source` column tags which producer wrote each row so manual entries (if
-- ever added) aren't touched by `prune=true`.
CREATE TABLE static_admins (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         CITEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'manifest',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX static_admins_source_idx ON static_admins (source);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS static_admins;
-- +goose StatementEnd
