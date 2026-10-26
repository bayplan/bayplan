-- +goose Up
-- +goose StatementBegin

-- Where a role assignment comes from: 'manual' (granted by an admin through
-- the API or by hand) or 'sso' (materialized by the OIDC login from
-- group_role_mappings, rbac.SyncSSORoleAssignments).
--
-- Before this column the SSO sync wrote ordinary rows, and the "no mapped
-- group, but maybe a manual assignment" branch of the login accepted them as
-- manual: a user removed from the IdP group kept the role the group had given
-- them forever. Now each login replaces the user's 'sso' rows with what their
-- current groups map to (none, if they left every group) and never touches
-- the 'manual' ones.
--
-- Existing rows cannot be told apart with certainty: an admin may have
-- granted a role by hand to an SSO user (the provider-without-groups case).
-- Leaving them all 'manual' would keep the bug alive for every SSO user that
-- already logged in, because the next sync finds the identical row and does
-- not duplicate it. So the rows that the old sync COULD have written are
-- marked 'sso': global scope (it never wrote another), of an 'oidc' user, and
-- with a role that some global group mapping grants today. The failure mode
-- is closed, not open: if one of those was in fact a manual grant and the
-- user's groups no longer map to it, the next login drops it and the admin
-- has to grant it again. The last global admin is never dropped (same guard
-- as before; see SyncSSORoleAssignments). Cluster-scoped rows and rows of
-- local users stay 'manual'.
ALTER TABLE role_assignments
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual'
    CONSTRAINT role_assignments_origin_check CHECK (origin IN ('manual', 'sso'));

UPDATE role_assignments ra
   SET origin = 'sso'
  FROM users u
 WHERE u.id = ra.user_id
   AND u.source = 'oidc'
   AND ra.scope_type = 'global'
   AND EXISTS (SELECT 1 FROM group_role_mappings gm
                WHERE gm.scope_type = 'global' AND gm.role = ra.role);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE role_assignments DROP COLUMN origin;
-- +goose StatementEnd
