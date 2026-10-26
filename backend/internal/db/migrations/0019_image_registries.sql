-- +goose Up
-- +goose StatementBegin

-- Private image registries (Docker Hub, GHCR, Harbor, Quay, ACR…). They are
-- platform-level, like the Helm repos: one credential, many clusters. The name
-- is used as the suffix of the Secret created in the cluster
-- (bayplan-registry-<name>), hence the DNS label CHECK.
CREATE TABLE image_registries (
    id                 BIGSERIAL PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$'),
    server             TEXT NOT NULL,
    username           TEXT NOT NULL,
    password_encrypted BYTEA NOT NULL,
    key_id             TEXT NOT NULL,
    email              TEXT,
    insecure_skip_tls  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A binding = the registry distributed to a namespace of a cluster: the panel
-- creates a kubernetes.io/dockerconfigjson Secret there and, if requested,
-- adds it to the imagePullSecrets of the `default` ServiceAccount (which is
-- what pods that declare no serviceAccountName use). Rotating the registry's
-- credential resyncs all of its bindings. Real FKs with CASCADE: a binding
-- without a registry or without a cluster means nothing.
CREATE TABLE registry_bindings (
    id                BIGSERIAL PRIMARY KEY,
    registry_id       BIGINT NOT NULL REFERENCES image_registries(id) ON DELETE CASCADE,
    cluster_id        BIGINT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    namespace         TEXT NOT NULL,
    attach_default_sa BOOLEAN NOT NULL DEFAULT TRUE,
    last_synced_at    TIMESTAMPTZ,
    last_error        TEXT,
    created_by        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (registry_id, cluster_id, namespace)
);
CREATE INDEX registry_bindings_cluster_idx ON registry_bindings (cluster_id);

CREATE TRIGGER trg_audit_db_image_registries
    AFTER INSERT OR UPDATE OR DELETE ON image_registries
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();
-- The state (last_*) is written by the syncer: outside the UPDATE trigger so
-- as not to flood audit_log every 15 minutes.
CREATE TRIGGER trg_audit_db_registry_bindings
    AFTER INSERT OR DELETE ON registry_bindings
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();
CREATE TRIGGER trg_audit_db_registry_bindings_update
    AFTER UPDATE OF registry_id, cluster_id, namespace, attach_default_sa ON registry_bindings
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS registry_bindings;
DROP TABLE IF EXISTS image_registries;
-- +goose StatementEnd
