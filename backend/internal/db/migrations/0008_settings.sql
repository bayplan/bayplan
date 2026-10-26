-- +goose Up
-- +goose StatementBegin

-- Instance configuration editable live from the UI.
--
-- Until now EVERYTHING that was not a cluster came from environment variables:
-- branding was hardcoded in the frontend and SSO required four AZURE_* and a
-- redeploy to change anything. For a product a third party deploys on their
-- own infrastructure that will not do: only the irreducible bootstrap stays
-- in the environment (DB, encryption key, port) and the rest (SSO, public
-- URL, branding, cloud credentials) lives in the DB, behind the API and
-- audited.
--
-- One row per SECTION (branding, server, auth.oidc…) with a JSONB document,
-- not one row per key: adding a setting within a section does not touch the
-- schema, same as users.preferences. The sensitive fields of a section (e.g.
-- the OIDC client secret) are encrypted by the backend with the same keyring
-- as the cluster tokens BEFORE storing them here; the JSON in the DB only
-- contains the ciphertext.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT
);

-- Same DB-level auditing as users/role_assignments/group_role_mappings: a
-- change made through psql leaves a trace even if it does not go through the
-- API. The whole row goes into the detail, but the secrets inside value are
-- already encrypted.
CREATE TRIGGER trg_audit_db_settings
    AFTER INSERT OR UPDATE OR DELETE ON settings
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

CREATE TRIGGER trg_audit_db_settings_truncate
    AFTER TRUNCATE ON settings
    FOR EACH STATEMENT EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS settings;
-- +goose StatementEnd
