-- +goose Up
-- +goose StatementBegin

-- GitOps sources: a git repository + branch + path whose manifests are applied
-- to a cluster (server-side apply, field manager "bayplan-gitops")
-- periodically or on receiving a webhook. It is "lightweight" GitOps, not a
-- full GitOps controller: no prune (what is deleted from the repo is not
-- deleted from the cluster, the UI warns about it), no application health, no
-- multi-cluster from a single source. Dedicated GitOps tools exist for that.
--
-- cluster_id IS an FK with ON DELETE CASCADE: unlike role_assignments, there
-- is no historical reason not to have it here and a source without a cluster
-- means nothing.
CREATE TABLE gitops_sources (
    id               BIGSERIAL PRIMARY KEY,
    cluster_id       BIGINT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    repo_url         TEXT NOT NULL,
    branch           TEXT NOT NULL DEFAULT 'main',
    path             TEXT NOT NULL DEFAULT '.',
    namespace        TEXT,                      -- default namespace for objects without one
    username         TEXT,
    token_encrypted  BYTEA,                     -- HTTPS token/password, encrypted with the keyring
    key_id           TEXT,
    interval_seconds INTEGER NOT NULL DEFAULT 300 CHECK (interval_seconds >= 30),
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    -- Webhook secret (hash, never plaintext): POST /api/gitops/webhook/{id}
    -- with the token in the header or query triggers a sync.
    webhook_token_hash TEXT NOT NULL,
    -- Last sync result (informational; the reconciler writes it, the UI reads
    -- it).
    last_sync_at     TIMESTAMPTZ,
    last_commit      TEXT,
    last_status      TEXT NOT NULL DEFAULT 'pending',  -- pending | synced | error
    last_error       TEXT,
    last_applied     JSONB,                     -- list of objects applied in the last sync
    created_by       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, name),
    CONSTRAINT gitops_key_id_present CHECK (token_encrypted IS NULL OR key_id IS NOT NULL)
);

-- DB auditing only for creations/deletions and configuration changes: the
-- state columns (last_*) are written by the reconciler every few minutes and
-- including them would flood audit_log, as happened with users.preferences.
CREATE TRIGGER trg_audit_db_gitops_sources
    AFTER INSERT OR DELETE ON gitops_sources
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();
CREATE TRIGGER trg_audit_db_gitops_sources_update
    AFTER UPDATE OF name, repo_url, branch, path, namespace, username, token_encrypted, interval_seconds, enabled, webhook_token_hash
    ON gitops_sources
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS gitops_sources;
-- +goose StatementEnd
