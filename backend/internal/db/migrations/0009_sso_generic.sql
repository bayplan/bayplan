-- +goose Up
-- +goose StatementBegin

-- SSO stops being exclusive to Entra ID: any OIDC provider. The column that
-- identified the user in the directory goes from "Entra object id" to "stable
-- provider subject" (in Entra it is still the oid, elsewhere the `sub`), and
-- the account source is renamed 'oidc'.
--
-- Existing users with source='entra' are migrated: they are OIDC users with a
-- specific provider, not another category. The UNIQUE is kept: it is still the
-- upsert key on every login.
ALTER TABLE users RENAME COLUMN entra_oid TO sso_subject;
ALTER TABLE users DROP CONSTRAINT users_source_check;
UPDATE users SET source = 'oidc' WHERE source = 'entra';
ALTER TABLE users ADD CONSTRAINT users_source_check CHECK (source IN ('local', 'oidc'));

-- The UPDATE audit trigger carries a column list (migration 0007) and named
-- entra_oid; Postgres updates the reference when the column is renamed, so
-- there is nothing to recreate.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP CONSTRAINT users_source_check;
UPDATE users SET source = 'entra' WHERE source = 'oidc';
ALTER TABLE users ADD CONSTRAINT users_source_check CHECK (source IN ('local', 'entra'));
ALTER TABLE users RENAME COLUMN sso_subject TO entra_oid;
-- +goose StatementEnd
