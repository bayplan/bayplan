-- +goose Up
-- +goose StatementBegin

-- 1) Allow deleting users without breaking audit_log immutability.
--
-- audit_log.user_id was created as REFERENCES users(id) with no ON DELETE
-- clause (0001 and again 0004), so any DELETE FROM users of a user with
-- history (all of them: every login is audited) failed with 23503. The two
-- usual ways out of an FK are ruled out here: ON DELETE CASCADE and ON DELETE
-- SET NULL run internally as row DELETE/UPDATE on audit_log, and the
-- trg_audit_log_immutable trigger blocks them. So the FK is dropped: from now
-- on audit_log.user_id may be orphaned on purpose (same decision as
-- audit_log.cluster, which stores the cluster's visible name to survive its
-- deletion).
--
-- The name really does carry the "1" suffix: 0004 renames the old table to
-- audit_log_unpartitioned WITHOUT renaming its constraint, so when the new
-- audit_log is created the name audit_log_user_id_fkey is already taken in
-- the schema and Postgres appends the suffix. It is deterministic: any DB
-- created from scratch reproduces the same name.
--
-- The DROP names it explicitly (no "DROP CONSTRAINT IF EXISTS <name>", which
-- would do nothing given a different name and the failure would show up
-- months later as an opaque 23503 on the first real deletion). It goes inside
-- an IF EXISTS *on the existence of any FK* so the migration can be applied
-- again after its own Down, which cannot restore the FK (see note in the
-- Down): if there is an FK, it is required to have that name; if there is
-- none, there is nothing to do.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'audit_log'::regclass AND contype = 'f') THEN
        ALTER TABLE audit_log DROP CONSTRAINT audit_log_user_id_fkey1;
    END IF;
END;
$$;

-- 2) Attribution that survives the deletion of the actor.
--
-- Without the FK, the user_id of a deleted user is a number pointing at
-- nothing. actor_username keeps who did what readable. Nullable column with no
-- backfill on purpose: ADD COLUMN like this is metadata-only DDL (it does not
-- rewrite rows, does not fire the immutability trigger —which only covers
-- UPDATE and DELETE— and propagates to every partition). Rows prior to this
-- migration stay NULL; their username is still recoverable by user_id as long
-- as the user exists.
ALTER TABLE audit_log ADD COLUMN actor_username TEXT;

-- 3) DB-level auditing of changes made through direct SQL.
--
-- audit.Log (Go) only sees what goes through the API. group_role_mappings has
-- no UI at all and is managed by hand through psql; a manual UPDATE users or
-- INSERT role_assignments left no trace either. These triggers close that gap
-- from the database itself, with the same defense-in-depth criterion as
-- trg_audit_log_immutable: the trail does not depend on the binary auditing
-- correctly.
--
-- Accepted consequences:
--   - Changes made by the app are DUPLICATED (user.create +
--     db.users.insert). Intentional: they are two independent trails.
--   - Only these 3 tables. sessions is left out because session.Validate does
--     an UPDATE of last_seen_at on EVERY request and the trigger would flood
--     audit_log. clusters is out too: its management is already audited at
--     the app level and the row contains the ServiceAccount's encrypted token.
--   - 'row' excludes password_hash: never put the bcrypt hash in the audit
--     trail (a no-op on the other two tables, which lack that column).
--   - inet_client_addr() is NULL when the connection comes in through a unix
--     socket, which is precisely the most likely case for a manual change
--     (podman exec bayplan-db psql ..., see deploy/README.md). And
--     application_name is reported by the client itself: it serves to tell
--     the app from an operator's psql, NOT as strong forensic proof.
--   - user_id is NULL because a DB trigger knows nothing of the HTTP session;
--     the actor is the database role (session_user).
CREATE FUNCTION audit_db_change() RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO audit_log (user_id, actor_username, action, result, detail)
    VALUES (
        NULL,
        session_user,
        'db.' || TG_TABLE_NAME || '.' || lower(TG_OP),
        'success',
        jsonb_build_object(
            'row', (CASE WHEN TG_OP = 'DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END) - 'password_hash',
            'dbUser', session_user,
            'app', current_setting('application_name', true),
            'clientAddr', inet_client_addr()
        )
    );
    RETURN NULL; -- AFTER trigger: the return value is ignored
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_db_users
    AFTER INSERT OR UPDATE OR DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

CREATE TRIGGER trg_audit_db_role_assignments
    AFTER INSERT OR UPDATE OR DELETE ON role_assignments
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

CREATE TRIGGER trg_audit_db_group_role_mappings
    AFTER INSERT OR UPDATE OR DELETE ON group_role_mappings
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- A FOR EACH ROW trigger never fires on TRUNCATE, so a manual "TRUNCATE
-- users" would leave no trace with only the ones above. These record that it
-- happened, without row detail (the same function works: to_jsonb(NEW) in a
-- statement trigger returns NULL, it does not fail).
CREATE TRIGGER trg_audit_db_users_truncate
    AFTER TRUNCATE ON users
    FOR EACH STATEMENT EXECUTE FUNCTION audit_db_change();

CREATE TRIGGER trg_audit_db_role_assignments_truncate
    AFTER TRUNCATE ON role_assignments
    FOR EACH STATEMENT EXECUTE FUNCTION audit_db_change();

CREATE TRIGGER trg_audit_db_group_role_mappings_truncate
    AFTER TRUNCATE ON group_role_mappings
    FOR EACH STATEMENT EXECUTE FUNCTION audit_db_change();

-- 4) Close the TRUNCATE bypass on audit_log.
--
-- trg_audit_log_immutable (0001, re-cloned in 0004) is BEFORE UPDATE OR
-- DELETE FOR EACH ROW, and TRUNCATE fires no row triggers: until this
-- migration, "TRUNCATE audit_log" emptied the append-only table without error
-- (verified). A statement trigger reusing the same exception function is
-- added.
--
-- Mind two partitioning details: TRUNCATE triggers are NOT cloned from the
-- parent to the partitions (unlike row triggers), and TRUNCATE on a specific
-- partition only fires that partition's trigger. That is why it has to be
-- created on the parent, on every existing partition, and also on every
-- future partition (hence the CREATE OR REPLACE of
-- audit_log_ensure_future_partitions below).
CREATE TRIGGER trg_audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable();

DO $$
DECLARE
    part record;
BEGIN
    FOR part IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'audit_log'
    LOOP
        EXECUTE format(
            'CREATE TRIGGER trg_audit_log_no_truncate BEFORE TRUNCATE ON %I
             FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable()',
            part.relname
        );
    END LOOP;
END;
$$;

-- Same function as 0004, plus the anti-TRUNCATE trigger on every partition it
-- creates (the backend invokes it on every startup, see
-- db.EnsureAuditLogPartitions): without this, future months' partitions would
-- be born without that protection.
CREATE OR REPLACE FUNCTION audit_log_ensure_future_partitions() RETURNS void AS $$
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
            EXECUTE format(
                'CREATE TRIGGER trg_audit_log_no_truncate BEFORE TRUNCATE ON %I
                 FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable()',
                partition_name
            );
        END IF;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The audit_log_user_id_fkey1 FK is NOT restored, and it is not an
-- oversight: it cannot be. "ADD CONSTRAINT ... NOT VALID" is not supported on
-- partitioned tables, and the validated variant fails as soon as an orphan row
-- exists — which is exactly the state this feature produces, and those rows
-- cannot be deleted because of the immutability trigger. This Down restores
-- the rest of the schema, but the absence of the FK is irreversible.
DROP TRIGGER IF EXISTS trg_audit_db_users_truncate ON users;
DROP TRIGGER IF EXISTS trg_audit_db_role_assignments_truncate ON role_assignments;
DROP TRIGGER IF EXISTS trg_audit_db_group_role_mappings_truncate ON group_role_mappings;
DROP TRIGGER IF EXISTS trg_audit_db_users ON users;
DROP TRIGGER IF EXISTS trg_audit_db_role_assignments ON role_assignments;
DROP TRIGGER IF EXISTS trg_audit_db_group_role_mappings ON group_role_mappings;
DROP FUNCTION IF EXISTS audit_db_change();

DO $$
DECLARE
    part record;
BEGIN
    FOR part IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'audit_log'
    LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS trg_audit_log_no_truncate ON %I', part.relname);
    END LOOP;
END;
$$;

DROP TRIGGER IF EXISTS trg_audit_log_no_truncate ON audit_log;

ALTER TABLE audit_log DROP COLUMN actor_username;

-- audit_log_ensure_future_partitions is left at this migration's version
-- (same criterion as 0005): going back to 0004's would reintroduce future
-- partitions without the anti-TRUNCATE protection. The trigger it creates is
-- idempotent with respect to this Down: if the Up is applied again, it does
-- not get in the way.

-- +goose StatementEnd
