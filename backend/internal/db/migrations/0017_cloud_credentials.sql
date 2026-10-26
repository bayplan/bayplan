-- +goose Up
-- +goose StatementBegin

-- Cloud provider credentials (AWS, Azure, GCP). With one credential the panel
-- DISCOVERS the provider's managed clusters (EKS/AKS/GKE) and connects to them
-- with short-lived tokens issued by the provider itself (STS presign, AAD,
-- Google OAuth2), instead of a 10-year ServiceAccount token pasted by hand. It
-- is the "cloud credentials" model: one credential,
-- many clusters, and rotating it rotates all of them at once.
--
--   - config: what is NOT sensitive (regions, subscription, tenant, client id,
--     project, resolved identity). The UI shows it.
--   - secret_encrypted: JSON with the sensitive part (secret access key,
--     client secret, service account JSON) encrypted with the keyring, same as
--     the cluster tokens. It never goes out to the frontend.
CREATE TABLE cloud_credentials (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    provider         TEXT NOT NULL CHECK (provider IN ('aws', 'azure', 'gcp')),
    config           JSONB NOT NULL DEFAULT '{}'::jsonb,
    secret_encrypted BYTEA NOT NULL,
    key_id           TEXT NOT NULL,
    created_by       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_audit_db_cloud_credentials
    AFTER INSERT OR UPDATE OR DELETE ON cloud_credentials
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- Third connector: 'cloud'. The cluster stores endpoint and CA (the provider
-- returns them when describing it and they are refreshed on every reconnect)
-- but NOT a credential of its own: the identity comes from
-- cloud_credential_id. ON DELETE RESTRICT on purpose: deleting a credential
-- with clusters hanging from it would leave clusters that cannot connect
-- without anyone knowing why.
ALTER TABLE clusters DROP CONSTRAINT clusters_connector_check;
ALTER TABLE clusters ADD CONSTRAINT clusters_connector_check CHECK (connector IN ('direct', 'agent', 'cloud'));
ALTER TABLE clusters ADD COLUMN cloud_credential_id BIGINT REFERENCES cloud_credentials(id) ON DELETE RESTRICT;
ALTER TABLE clusters ADD COLUMN cloud_ref JSONB;

ALTER TABLE clusters DROP CONSTRAINT clusters_has_credential;
ALTER TABLE clusters ADD CONSTRAINT clusters_has_credential
    CHECK (connector IN ('agent', 'cloud') OR sa_token_encrypted IS NOT NULL OR client_key_encrypted IS NOT NULL);
ALTER TABLE clusters ADD CONSTRAINT clusters_cloud_has_ref
    CHECK (connector <> 'cloud' OR (cloud_credential_id IS NOT NULL AND cloud_ref IS NOT NULL));

CREATE INDEX clusters_cloud_credential_idx ON clusters (cloud_credential_id) WHERE cloud_credential_id IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM clusters WHERE connector = 'cloud';
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_cloud_has_ref;
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_has_credential;
ALTER TABLE clusters ADD CONSTRAINT clusters_has_credential
    CHECK (connector = 'agent' OR sa_token_encrypted IS NOT NULL OR client_key_encrypted IS NOT NULL);
ALTER TABLE clusters DROP COLUMN cloud_ref;
ALTER TABLE clusters DROP COLUMN cloud_credential_id;
ALTER TABLE clusters DROP CONSTRAINT clusters_connector_check;
ALTER TABLE clusters ADD CONSTRAINT clusters_connector_check CHECK (connector IN ('direct', 'agent'));
DROP TABLE IF EXISTS cloud_credentials;
-- +goose StatementEnd
