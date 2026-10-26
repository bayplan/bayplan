-- +goose Up
-- +goose StatementBegin

-- Fixes a real bug in audit_log_drop_old_partitions (0004): Postgres
-- `substring(str FROM pattern)` with a regular expression that has MORE than
-- one capture group returns only the FIRST group, not the concatenation of
-- all of them. The original pattern 'y(\d{4})m(\d{2})' has two groups, so
-- `substring(...)` returned only the year ("2026") for every partition,
-- regardless of the month — `to_date('2026', 'YYYYMM')` interpreted that as
-- January of that year for ALL partitions, and with any reasonable retention
-- the `part_month < cutoff` comparison was true for the current month's
-- partition too, purging it by mistake.
--
-- Verified in manual tests: with a '0 days' retention, the function dropped
-- both the next month's partition and the current month's (with real data
-- inside). Fixed here with regexp_match, which does return every capture
-- group as an array.
CREATE OR REPLACE FUNCTION audit_log_drop_old_partitions(retention interval DEFAULT '1 month') RETURNS void AS $$
DECLARE
    part record;
    cutoff date := date_trunc('month', now() - retention);
    part_month date;
    m text[];
BEGIN
    FOR part IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'audit_log' AND c.relname LIKE 'audit_log_y%'
    LOOP
        m := regexp_match(part.relname, 'y(\d{4})m(\d{2})');
        IF m IS NULL THEN
            CONTINUE; -- unexpected name: do not touch, better to err on the conservative side
        END IF;
        part_month := to_date(m[1] || m[2], 'YYYYMM');
        IF part_month < cutoff THEN
            EXECUTE format('DROP TABLE %I', part.relname);
        END IF;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

-- The current month's partition may have been dropped by the bug above in
-- deployments that had already invoked the function with real data within the
-- tested retention window; here we only make sure it exists again going
-- forward (data already lost is not recoverable from this migration — see the
-- operational note in the commit/PR message: if this ever ran in an
-- environment with real data, restore from a Postgres backup).
SELECT audit_log_ensure_future_partitions();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- There is no sensible way back to the buggy version: leaving the fixed
-- function in the Down as well avoids reintroducing a known data-loss bug.
-- +goose StatementEnd
