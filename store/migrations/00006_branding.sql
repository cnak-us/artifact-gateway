-- +goose Up
-- +goose StatementBegin

-- branding holds the admin-editable runtime white-label overrides. Exactly
-- one row exists for the lifetime of the deployment (id = 1, enforced by the
-- CHECK constraint), so UPDATE … WHERE id=1 is always a no-FK upsert. Every
-- field is NULL/empty by default; the UI loader merges this response over the
-- compiled-in CNAK preset, so empty/NULL means "fall through to the preset".
CREATE TABLE branding (
    id                   SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    product_name         TEXT,
    vendor               TEXT,
    vendor_short         TEXT,
    footer_tagline       TEXT,
    embedded_tagline     TEXT,
    catalog_hero_eyebrow TEXT,
    html_title           TEXT,
    meta_description     TEXT,
    accent_light_main    TEXT,  -- "R G B" triplet, e.g. "56 113 220"
    accent_light_text    TEXT,
    accent_dark_main     TEXT,
    accent_dark_text     TEXT,
    logo_svg             TEXT,  -- raw <svg>...</svg> markup, may be large
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by           TEXT
);

-- Guarantee the single row exists so GET/PUT never have to INSERT.
INSERT INTO branding (id) VALUES (1);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS branding;
-- +goose StatementEnd
