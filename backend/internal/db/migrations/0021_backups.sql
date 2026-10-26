-- +goose Up
-- +goose StatementBegin

-- CONFIGURATION backups (manifests) to S3-compatible storage (AWS S3, MinIO,
-- Ceph RGW, Wasabi…). They do not copy volumes: Velero exists for that, and
-- the UI says so. What they copy is what would be needed to recreate the
-- namespaces in another cluster: deployments, services, configmaps, secrets
-- (optional), ingresses, quotas… cleaned of state.
--
-- Platform-level targets (one S3 account, many schedules); secret encrypted
-- with the keyring.
CREATE TABLE backup_targets (
    id                BIGSERIAL PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    endpoint          TEXT,
    region            TEXT NOT NULL DEFAULT 'us-east-1',
    bucket            TEXT NOT NULL,
    prefix            TEXT NOT NULL DEFAULT '',
    access_key_id     TEXT NOT NULL,
    secret_encrypted  BYTEA NOT NULL,
    key_id            TEXT NOT NULL,
    path_style        BOOLEAN NOT NULL DEFAULT FALSE,
    insecure_skip_tls BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A schedule = cluster + scope + target + how often + how many to keep.
-- interval_hours = 0 is "manual only". The target cannot be deleted while it
-- has schedules (RESTRICT); the cluster can (CASCADE).
CREATE TABLE backup_schedules (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    cluster_id      BIGINT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    target_id       BIGINT NOT NULL REFERENCES backup_targets(id) ON DELETE RESTRICT,
    namespaces      JSONB NOT NULL DEFAULT '[]'::jsonb,
    include_secrets BOOLEAN NOT NULL DEFAULT FALSE,
    interval_hours  INT NOT NULL DEFAULT 24 CHECK (interval_hours >= 0),
    retention       INT NOT NULL DEFAULT 14 CHECK (retention >= 1),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    last_run_at     TIMESTAMPTZ,
    created_by      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, name)
);

-- Run history: what the UI lists to download or restore. object_key is the
-- path in the bucket; a failed run has none.
CREATE TABLE backup_runs (
    id          BIGSERIAL PRIMARY KEY,
    schedule_id BIGINT NOT NULL REFERENCES backup_schedules(id) ON DELETE CASCADE,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    status      TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'ok', 'error')),
    trigger     TEXT NOT NULL DEFAULT 'schedule' CHECK (trigger IN ('schedule', 'manual')),
    object_key  TEXT,
    size_bytes  BIGINT,
    objects     INT,
    skipped     JSONB,
    error       TEXT
);
CREATE INDEX backup_runs_schedule_idx ON backup_runs (schedule_id, started_at DESC);

CREATE TRIGGER trg_audit_db_backup_targets
    AFTER INSERT OR UPDATE OR DELETE ON backup_targets
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();
CREATE TRIGGER trg_audit_db_backup_schedules
    AFTER INSERT OR DELETE ON backup_schedules
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();
-- last_run_at is written by the scheduler: outside the UPDATE trigger.
CREATE TRIGGER trg_audit_db_backup_schedules_update
    AFTER UPDATE OF name, target_id, namespaces, include_secrets, interval_hours, retention, enabled ON backup_schedules
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS backup_runs;
DROP TABLE IF EXISTS backup_schedules;
DROP TABLE IF EXISTS backup_targets;
-- +goose StatementEnd
