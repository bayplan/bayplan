-- +goose Up
-- +goose StatementBegin

-- Backups of Bayplan's OWN database (pg_dump to an S3 target), as opposed to
-- backup_runs, which are manifests of a managed cluster. Without this the
-- only copy of users, assignments, clusters and the audit log was whatever the
-- operator remembered to do with the volume.
--
-- The schedule lives in settings ("serverBackup" section); this table is only
-- the history the UI shows, pruned to the last 100 runs. It is not audit: the
-- runs and downloads are audited in audit_log.
CREATE TABLE server_backup_runs (
    id          BIGSERIAL PRIMARY KEY,
    trigger     TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual')),
    target_name TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'ok', 'error')),
    object_key  TEXT,
    size_bytes  BIGINT,
    error       TEXT,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX server_backup_runs_started_idx ON server_backup_runs (started_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE server_backup_runs;
-- +goose StatementEnd
