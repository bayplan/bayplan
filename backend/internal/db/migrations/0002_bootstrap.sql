-- +goose Up
-- +goose StatementBegin

-- Relaxes the original constraint: a local user may have no password YET if
-- they are pending completion of the initial setup flow (must_change_password
-- = TRUE and no password_hash). As soon as they complete the setup,
-- password_hash becomes mandatory again.
ALTER TABLE users DROP CONSTRAINT users_local_has_password;
ALTER TABLE users ADD CONSTRAINT users_local_has_password
    CHECK (source <> 'local' OR password_hash IS NOT NULL OR must_change_password);

-- One-time initial setup token. Instead of generating and printing an
-- administrator password in the log (which stays permanently in journald),
-- the first startup generates this token: the admin uses it exactly once in
-- /setup to set their own password. The token never works as a login
-- password.
CREATE TABLE bootstrap_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS bootstrap_tokens;
ALTER TABLE users DROP CONSTRAINT users_local_has_password;
ALTER TABLE users ADD CONSTRAINT users_local_has_password
    CHECK (source <> 'local' OR password_hash IS NOT NULL);
-- +goose StatementEnd
