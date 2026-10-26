-- +goose Up
-- +goose StatementBegin

-- Helm chart repositories (HTTP with index.yaml, or OCI). They are
-- platform-level, not per cluster: the same catalog serves to install on any
-- of them. Credentials are encrypted with the keyring (same key as the cluster
-- tokens); the username in plaintext because it is not secret and the UI shows
-- it.
CREATE TABLE helm_repos (
    id                 BIGSERIAL PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    url                TEXT NOT NULL,
    username           TEXT,
    password_encrypted BYTEA,
    key_id             TEXT,
    insecure_skip_tls  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT helm_repos_key_id_present CHECK (password_encrypted IS NULL OR key_id IS NOT NULL)
);

-- Same DB auditing as the rest of the configuration tables.
CREATE TRIGGER trg_audit_db_helm_repos
    AFTER INSERT OR UPDATE OR DELETE ON helm_repos
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS helm_repos;
-- +goose StatementEnd
