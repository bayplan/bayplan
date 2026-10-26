-- +goose Up
-- +goose StatementBegin

-- audit_log retention: 1 month (business decision). The immutability trigger
-- (trg_audit_log_immutable, migration 0001) blocks EVERY UPDATE/DELETE without
-- exception, so a bulk DELETE by age would fail against our own protection —
-- and even if it did not, a DELETE of millions of rows leaves bloat that has
-- to be VACUUMed afterwards.
--
-- Solution: partition audit_log by range of "ts" (one partition per month).
-- BEFORE ROW triggers defined on the parent table are automatically cloned
-- into each partition (Postgres >= 11), so immutability is kept unchanged.
-- "Purging" old data becomes a DROP TABLE of the whole partition: a DDL
-- operation that fires no row triggers, consistent with "delete by old
-- blocks", not row by row.

ALTER TABLE audit_log RENAME TO audit_log_unpartitioned;

CREATE TABLE audit_log (
    id         BIGSERIAL,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id    BIGINT REFERENCES users(id),
    ip         INET,
    action     TEXT NOT NULL,
    cluster    TEXT,
    namespace  TEXT,
    resource   TEXT,
    result     TEXT NOT NULL,
    detail     JSONB,
    PRIMARY KEY (id, ts) -- a partitioned table requires the partition key (ts) to be part of the PK
) PARTITION BY RANGE (ts);

-- Fallback partition: any row whose ts falls in no explicit partition
-- (historical test data, odd clocks, etc.) lands here instead of the
-- INSERT/COPY failing.
CREATE TABLE audit_log_default PARTITION OF audit_log DEFAULT;

-- Idempotent maintenance function: creates the partition for the current
-- month and the next one if they do not exist. Dynamic (uses now()) so as not
-- to depend on the date this migration runs. It is invoked from two places
-- with different purposes:
--   - Go, on every backend startup (see internal/db/db.go): only this
--     "ensure future partitions" part — self-correcting, additive, it never
--     deletes anything on its own at startup.
--   - A periodic external job (cron/pg_cron, see deploy/README.md), which
--     also invokes audit_log_drop_old_partitions(): the destructive part of
--     retention lives outside the app's startup cycle on purpose, so that a
--     bug in the binary can never purge audit data by accident on every
--     deployment.
CREATE FUNCTION audit_log_ensure_future_partitions() RETURNS void AS $$
DECLARE
    i int;
    month_start date;
    partition_name text;
BEGIN
    FOR i IN 0..1 LOOP
        month_start := date_trunc('month', now() + (i || ' month')::interval);
        partition_name := 'audit_log_y' || to_char(month_start, 'YYYY') || 'm' || to_char(month_start, 'MM');
        IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = partition_name) THEN
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF audit_log FOR VALUES FROM (%L) TO (%L)',
                partition_name, month_start, month_start + interval '1 month'
            );
        END IF;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

-- Removes (DROP TABLE) the monthly partitions older than the retention
-- period. It is not invoked from the app's startup, only from the external
-- job documented in deploy/README.md.
CREATE FUNCTION audit_log_drop_old_partitions(retention interval DEFAULT '1 month') RETURNS void AS $$
DECLARE
    part record;
    cutoff date := date_trunc('month', now() - retention);
    part_month date;
BEGIN
    FOR part IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'audit_log' AND c.relname LIKE 'audit_log_y%'
    LOOP
        BEGIN
            part_month := to_date(substring(part.relname FROM 'y(\d{4})m(\d{2})'), 'YYYYMM');
        EXCEPTION WHEN OTHERS THEN
            CONTINUE; -- unexpected name: do not touch, better to err on the conservative side
        END;
        IF part_month < cutoff THEN
            EXECUTE format('DROP TABLE %I', part.relname);
        END IF;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

-- Creates the current month's partition (and the next one) BEFORE copying the
-- data, so that this month's existing rows end up in their real monthly
-- partition and not in the fallback one.
SELECT audit_log_ensure_future_partitions();

INSERT INTO audit_log (id, ts, user_id, ip, action, cluster, namespace, resource, result, detail)
    SELECT id, ts, user_id, ip, action, cluster, namespace, resource, result, detail
    FROM audit_log_unpartitioned;

-- Renaming a table does NOT rename its indexes: audit_log_unpartitioned still
-- has indexes named idx_audit_log_ts/idx_audit_log_user_id, which would clash
-- with the ones created next unless the old table is dropped first (it is no
-- longer needed, the data is already copied).
DROP TABLE audit_log_unpartitioned;

-- Indexes created on the partitioned table propagate automatically to every
-- existing partition and to those added later with "CREATE TABLE ... PARTITION
-- OF" (standard behavior since Postgres 11), so there is no need to recreate
-- them per partition.
CREATE INDEX idx_audit_log_ts ON audit_log(ts);
CREATE INDEX idx_audit_log_user_id ON audit_log(user_id);

-- The BEFORE UPDATE OR DELETE trigger defined on the parent table is cloned
-- just as automatically into each partition: it remains impossible to modify
-- or delete an individual row, with or without partitioning.
CREATE TRIGGER trg_audit_log_immutable
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE audit_log RENAME TO audit_log_partitioned;

CREATE TABLE audit_log (
    id         BIGSERIAL PRIMARY KEY,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id    BIGINT REFERENCES users(id),
    ip         INET,
    action     TEXT NOT NULL,
    cluster    TEXT,
    namespace  TEXT,
    resource   TEXT,
    result     TEXT NOT NULL,
    detail     JSONB
);

INSERT INTO audit_log (id, ts, user_id, ip, action, cluster, namespace, resource, result, detail)
    SELECT id, ts, user_id, ip, action, cluster, namespace, resource, result, detail
    FROM audit_log_partitioned ORDER BY ts;

CREATE INDEX idx_audit_log_ts ON audit_log(ts);
CREATE INDEX idx_audit_log_user_id ON audit_log(user_id);

CREATE TRIGGER trg_audit_log_immutable
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

DROP TABLE audit_log_partitioned;
DROP FUNCTION IF EXISTS audit_log_drop_old_partitions(interval);
DROP FUNCTION IF EXISTS audit_log_ensure_future_partitions();
-- +goose StatementEnd
