-- +goose Up
-- +goose StatementBegin

-- 1) Profile data.
--
-- users only had username: no display name, no email. The Entra ID email was
-- already arriving in the ID token and being discarded (upsertEntraUser only
-- persisted username, source and entra_oid), so the information existed and
-- was being thrown away.
--
-- email WITHOUT UNIQUE on purpose: local users may have none, and the same
-- shared mailbox may legitimately appear on two accounts. Identity is still
-- username (local) or entra_oid (SSO), which are already UNIQUE.
ALTER TABLE users ADD COLUMN display_name TEXT;
ALTER TABLE users ADD COLUMN email TEXT;

-- 2) UI preferences, a single JSON document per user.
--
-- One document and not one row per preference: one resource per preference
-- (3-10 objects per user) is a model that is hard to evolve and tends to be
-- deprecated. Grafana's pattern (one blob per user + partial PATCH) is
-- simpler and allows adding a preference without a migration.
--
-- NOT NULL DEFAULT '{}' so the backend never has to distinguish between "no
-- preferences" and NULL.
ALTER TABLE users ADD COLUMN preferences JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 3) Saving a preference must not flood audit_log.
--
-- trg_audit_db_users (migration 0006) writes an audit row on EVERY UPDATE of
-- users. With preferences in this table, changing the theme or the language
-- would generate one audit entry per keystroke, and with the whole row in the
-- detail on top: noise that buries what really matters to audit (creations,
-- deletions, disabled accounts, password changes).
--
-- The solution is the UPDATE OF column list: the trigger only fires if the
-- statement mentions one of those columns in its SET. An
-- "UPDATE users SET preferences = ..., updated_at = now()" does not touch them
-- and is not audited; any change of identity, credential or state is.
--
-- Mind these details when maintaining this:
--   - updated_at is left OUT of the list on purpose: if it were in, any update
--     would mention it and the exclusion would be useless.
--   - UPDATE OF fires on MENTIONING the column in the SET, even if the value
--     does not change. It is more conservative (audits too much, never too
--     little), which is what one wants in a security trail.
--   - display_name and email ARE audited: they are identity data of the
--     person, not an interface preference.
DROP TRIGGER IF EXISTS trg_audit_db_users ON users;
CREATE TRIGGER trg_audit_db_users
    AFTER INSERT OR DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();
CREATE TRIGGER trg_audit_db_users_update
    AFTER UPDATE OF username, password_hash, must_change_password, source, entra_oid, disabled, display_name, email
    ON users
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS trg_audit_db_users_update ON users;
DROP TRIGGER IF EXISTS trg_audit_db_users ON users;
-- The trigger is restored as 0006 left it: a single AFTER INSERT OR UPDATE OR
-- DELETE with no column list.
CREATE TRIGGER trg_audit_db_users
    AFTER INSERT OR UPDATE OR DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

ALTER TABLE users DROP COLUMN preferences;
ALTER TABLE users DROP COLUMN email;
ALTER TABLE users DROP COLUMN display_name;

-- +goose StatementEnd
