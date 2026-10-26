-- +goose Up
-- +goose StatementBegin

-- The UNIQUE (…, scope_type, scope_ref) of role_assignments and
-- group_role_mappings (0001) never deduplicated 'global' rows: scope_ref is
-- NULL there and Postgres treats every NULL as distinct. GrantRole and the
-- group mapping Create compensated with a SELECT … IS NOT DISTINCT FROM before
-- the INSERT, which is not atomic: two concurrent requests both see "does not
-- exist" and both insert. Partial unique indexes over the NULL case close
-- that race in the database, and let the code use ON CONFLICT DO NOTHING.
--
-- Twin rows that already slipped through are collapsed first, keeping the
-- oldest (MIN(id)). They are identical by definition (same user/group, role
-- and scope), so nothing is lost; the DELETEs on role_assignments and
-- group_role_mappings go through trg_audit_db_* and leave their trace.
DELETE FROM role_assignments ra
 USING role_assignments keep
 WHERE ra.scope_ref IS NULL AND keep.scope_ref IS NULL
   AND ra.user_id = keep.user_id AND ra.role = keep.role AND ra.scope_type = keep.scope_type
   AND ra.id > keep.id;

DELETE FROM group_role_mappings gm
 USING group_role_mappings keep
 WHERE gm.scope_ref IS NULL AND keep.scope_ref IS NULL
   AND gm.ad_group_id = keep.ad_group_id AND gm.role = keep.role AND gm.scope_type = keep.scope_type
   AND gm.id > keep.id;

CREATE UNIQUE INDEX role_assignments_null_scope_unique
    ON role_assignments (user_id, role, scope_type) WHERE scope_ref IS NULL;

CREATE UNIQUE INDEX group_role_mappings_null_scope_unique
    ON group_role_mappings (ad_group_id, role, scope_type) WHERE scope_ref IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS group_role_mappings_null_scope_unique;
DROP INDEX IF EXISTS role_assignments_null_scope_unique;
-- +goose StatementEnd
