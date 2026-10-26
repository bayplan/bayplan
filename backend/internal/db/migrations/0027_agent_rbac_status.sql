-- +goose Up
-- +goose StatementBegin

-- The agent keeps its own RBAC up to date: at startup and every few hours it
-- asks the server for the current RBAC for its cluster and applies it with its
-- SA (server-side apply). Here it stores which version (hash) it applied, when
-- and the last error, so the UI can say whether the cluster is up to date or
-- needs the manifest again.
ALTER TABLE clusters ADD COLUMN agent_rbac_hash TEXT;
ALTER TABLE clusters ADD COLUMN agent_rbac_applied_at TIMESTAMPTZ;
ALTER TABLE clusters ADD COLUMN agent_rbac_error TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE clusters DROP COLUMN agent_rbac_error;
ALTER TABLE clusters DROP COLUMN agent_rbac_applied_at;
ALTER TABLE clusters DROP COLUMN agent_rbac_hash;
-- +goose StatementEnd
