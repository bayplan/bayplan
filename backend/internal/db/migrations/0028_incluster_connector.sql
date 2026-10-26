-- +goose Up
-- +goose StatementBegin

-- "incluster" connector: the cluster the server itself runs in (the
-- `local` cluster). It registers itself at startup inside Kubernetes
-- (LOCAL_CLUSTER=true, set by the chart) and uses the server pod's
-- ServiceAccount: neither a credential in the DB nor an agent. It cannot be
-- deleted nor can its connection be edited from the API.
ALTER TABLE clusters DROP CONSTRAINT clusters_connector_check;
ALTER TABLE clusters ADD CONSTRAINT clusters_connector_check CHECK (connector IN ('direct', 'agent', 'cloud', 'incluster'));
ALTER TABLE clusters DROP CONSTRAINT clusters_has_credential;
ALTER TABLE clusters ADD CONSTRAINT clusters_has_credential
    CHECK (connector IN ('agent', 'cloud', 'incluster') OR sa_token_encrypted IS NOT NULL OR client_key_encrypted IS NOT NULL);
-- There can be only one incluster cluster: it is "this one".
CREATE UNIQUE INDEX clusters_single_incluster ON clusters ((connector)) WHERE connector = 'incluster';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM clusters WHERE connector = 'incluster';
DROP INDEX IF EXISTS clusters_single_incluster;
ALTER TABLE clusters DROP CONSTRAINT clusters_has_credential;
ALTER TABLE clusters ADD CONSTRAINT clusters_has_credential
    CHECK (connector IN ('agent', 'cloud') OR sa_token_encrypted IS NOT NULL OR client_key_encrypted IS NOT NULL);
ALTER TABLE clusters DROP CONSTRAINT clusters_connector_check;
ALTER TABLE clusters ADD CONSTRAINT clusters_connector_check CHECK (connector IN ('direct', 'agent', 'cloud'));
-- +goose StatementEnd
