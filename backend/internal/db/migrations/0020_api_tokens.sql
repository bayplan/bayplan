-- +goose Up
-- +goose StatementBegin

-- Personal API tokens (like GitHub PATs): a user creates
-- a token, uses it in `Authorization: Bearer bpl_…` from scripts/CI, and the
-- token acts WITH THEIR permissions (never more). `read_only` additionally
-- caps it to readonly on every cluster, for automations that only query. Only
-- the hash (SHA-256) is stored, same as sessions; the plaintext prefix is so
-- the user can tell which is which. Deleting the user deletes their tokens.
CREATE TABLE api_tokens (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    read_only    BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
CREATE INDEX api_tokens_user_idx ON api_tokens (user_id);

-- DB auditing: creation, deletion and revocation. last_used_at is left out:
-- it is updated on every request authenticated with the token.
CREATE TRIGGER trg_audit_db_api_tokens
    AFTER INSERT OR DELETE ON api_tokens
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();
CREATE TRIGGER trg_audit_db_api_tokens_update
    AFTER UPDATE OF name, read_only, expires_at, revoked_at ON api_tokens
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS api_tokens;
-- +goose StatementEnd
