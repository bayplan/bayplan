-- +goose Up
-- +goose StatementBegin

-- 0028 exempted the incluster connector from clusters_has_credential but not
-- from clusters_has_ca, so registering the local cluster on a fresh chart
-- install failed and the server crash-looped. Its CA, like its token, comes
-- from the pod (rest.InClusterConfig), never from the DB.
ALTER TABLE clusters DROP CONSTRAINT clusters_has_ca;
ALTER TABLE clusters ADD CONSTRAINT clusters_has_ca
    CHECK (connector IN ('agent', 'incluster') OR ca_cert IS NOT NULL OR insecure_skip_tls_verify);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE clusters DROP CONSTRAINT clusters_has_ca;
ALTER TABLE clusters ADD CONSTRAINT clusters_has_ca
    CHECK (connector = 'agent' OR ca_cert IS NOT NULL OR insecure_skip_tls_verify);
-- +goose StatementEnd
