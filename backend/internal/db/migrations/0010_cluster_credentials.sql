-- +goose Up
-- +goose StatementBegin

-- Until now a cluster could only be registered with endpoint + CA +
-- ServiceAccount token, all three mandatory and all three pasted by hand. A
-- generic product needs to accept what the customer already has: a kubeconfig
-- (kubeadm, k3s, RKE2 and Talos authenticate with a client certificate, not a
-- token) and lab clusters with a self-signed certificate and no exportable CA.
--
-- Hence:
--   - ca_cert becomes optional, but ONLY if insecure_skip_tls_verify is on
--     (clusters_has_ca constraint): never a cluster without CA and without
--     having explicitly accepted the risk.
--   - A client certificate is accepted as an alternative to the token. The
--     private key is encrypted with the same keyring as the token; the
--     certificate is public and goes in plaintext. At least one of the two
--     credentials must exist (clusters_has_credential).
--   - description: free text for the admin ("EU production, on-prem").
ALTER TABLE clusters ALTER COLUMN ca_cert DROP NOT NULL;
ALTER TABLE clusters ALTER COLUMN sa_token_encrypted DROP NOT NULL;
ALTER TABLE clusters ALTER COLUMN key_id DROP NOT NULL;
ALTER TABLE clusters ADD COLUMN insecure_skip_tls_verify BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE clusters ADD COLUMN client_cert TEXT;
ALTER TABLE clusters ADD COLUMN client_key_encrypted BYTEA;
ALTER TABLE clusters ADD COLUMN description TEXT;

ALTER TABLE clusters ADD CONSTRAINT clusters_has_ca
    CHECK (ca_cert IS NOT NULL OR insecure_skip_tls_verify);
ALTER TABLE clusters ADD CONSTRAINT clusters_has_credential
    CHECK (sa_token_encrypted IS NOT NULL OR client_key_encrypted IS NOT NULL);
-- key_id accompanies any encrypted data: if there is a token or key, it must be present.
ALTER TABLE clusters ADD CONSTRAINT clusters_key_id_present
    CHECK ((sa_token_encrypted IS NULL AND client_key_encrypted IS NULL) OR key_id IS NOT NULL);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_key_id_present;
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_has_credential;
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_has_ca;
ALTER TABLE clusters DROP COLUMN description;
ALTER TABLE clusters DROP COLUMN client_key_encrypted;
ALTER TABLE clusters DROP COLUMN client_cert;
ALTER TABLE clusters DROP COLUMN insecure_skip_tls_verify;
-- The NOT NULLs are not restored: there could be rows that already violate them.
-- +goose StatementEnd
