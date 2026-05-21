-- +goose Up
-- +goose StatementBegin

-- root_keys holds Ed25519 signing keys for license issuance. private_key_enc
-- is KEK-wrapped via auth.Crypto (same envelope as upstream_credentials.pat_enc);
-- NULL private_key_enc means the row is verify-only (imported pubkey, no signing).
-- The partial unique index enforces at most one active signing key at a time.
CREATE TABLE root_keys (
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

CREATE UNIQUE INDEX root_keys_one_active_idx ON root_keys (active) WHERE active = TRUE;

-- Seed the legacy cnaklic public key as a verify-only root so previously-issued
-- .lic files continue to verify after the cutover. Bytes mirror
-- cnak/pkg/license/pubkey.go. fingerprint = first 16 hex chars of sha256(pubkey).
INSERT INTO root_keys (name, public_key, private_key_enc, fingerprint, active, imported_from)
VALUES (
    'cnaklic-legacy',
    decode('771c72e4f6ea354aa02047283cbd1510bd9c43aa2931fa5ccda23fd0e1fef0b3', 'hex'),
    NULL,
    substring(encode(digest(decode('771c72e4f6ea354aa02047283cbd1510bd9c43aa2931fa5ccda23fd0e1fef0b3', 'hex'), 'sha256'), 'hex'), 1, 16),
    FALSE,
    'cnaklic-legacy'
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS root_keys;
-- +goose StatementEnd
