-- +goose Up
-- +goose StatementBegin

-- Agent connector: instead of the server reaching the cluster's API server
-- ('direct' connector, the only one until now), an agent deployed in the
-- cluster opens an OUTBOUND WebSocket connection to the server and the server
-- talks to the API server through that tunnel. It is the usual model for
-- fleet management: it works with clusters behind NAT, on
-- another network or with a private endpoint, and there is no need to paste a
-- CA or a token: the agent uses its own ServiceAccount.
--
--   - agent_key_hash: hash of the agent's long-lived credential (generated
--     when the cluster is created and embedded in the manifest).
--   - manifest_token_*: short-lived token (1 h, reusable within that window
--     in case the first kubectl apply fails halfway) that protects the
--     manifest URL: the long key never goes in a URL that could end up in
--     logs.
--   - agent_last_seen: last time the tunnel was open.
ALTER TABLE clusters ADD COLUMN connector TEXT NOT NULL DEFAULT 'direct' CHECK (connector IN ('direct', 'agent'));
ALTER TABLE clusters ADD COLUMN agent_key_hash TEXT;
ALTER TABLE clusters ADD COLUMN manifest_token_hash TEXT;
ALTER TABLE clusters ADD COLUMN manifest_token_expires_at TIMESTAMPTZ;
ALTER TABLE clusters ADD COLUMN agent_last_seen TIMESTAMPTZ;

-- An agent cluster has no credentials or CA of its own: the constraints from
-- 0010 now apply only to the direct connector.
ALTER TABLE clusters DROP CONSTRAINT clusters_has_ca;
ALTER TABLE clusters DROP CONSTRAINT clusters_has_credential;
ALTER TABLE clusters ADD CONSTRAINT clusters_has_ca
    CHECK (connector = 'agent' OR ca_cert IS NOT NULL OR insecure_skip_tls_verify);
ALTER TABLE clusters ADD CONSTRAINT clusters_has_credential
    CHECK (connector = 'agent' OR sa_token_encrypted IS NOT NULL OR client_key_encrypted IS NOT NULL);
ALTER TABLE clusters ADD CONSTRAINT clusters_agent_has_key
    CHECK (connector <> 'agent' OR agent_key_hash IS NOT NULL);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_agent_has_key;
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_has_credential;
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_has_ca;
DELETE FROM clusters WHERE connector = 'agent';
ALTER TABLE clusters ADD CONSTRAINT clusters_has_ca CHECK (ca_cert IS NOT NULL OR insecure_skip_tls_verify);
ALTER TABLE clusters ADD CONSTRAINT clusters_has_credential CHECK (sa_token_encrypted IS NOT NULL OR client_key_encrypted IS NOT NULL);
ALTER TABLE clusters DROP COLUMN agent_last_seen;
ALTER TABLE clusters DROP COLUMN manifest_token_expires_at;
ALTER TABLE clusters DROP COLUMN manifest_token_hash;
ALTER TABLE clusters DROP COLUMN agent_key_hash;
ALTER TABLE clusters DROP COLUMN connector;
-- +goose StatementEnd
