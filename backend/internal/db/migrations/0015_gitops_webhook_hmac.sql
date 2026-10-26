-- +goose Up
-- +goose StatementBegin

-- The webhook token is also stored encrypted (besides the hash): it is needed
-- in plaintext to verify the HMAC signature sent by GitHub
-- (X-Hub-Signature-256) and Bitbucket, which do not allow custom headers. This
-- way the token never travels in the URL (it would end up in the proxy's
-- access logs).
ALTER TABLE gitops_sources ADD COLUMN webhook_token_encrypted BYTEA;
ALTER TABLE gitops_sources ADD COLUMN webhook_key_id TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE gitops_sources DROP COLUMN webhook_key_id;
ALTER TABLE gitops_sources DROP COLUMN webhook_token_encrypted;
-- +goose StatementEnd
