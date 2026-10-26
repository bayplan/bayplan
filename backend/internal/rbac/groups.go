package rbac

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SSOGrant is one role the user's IdP groups give them: global scope
// (ScopeRef "") or 'cluster' scope on the cluster id in ScopeRef.
type SSOGrant struct {
	Role      Role
	ScopeType string // "global" | "cluster"
	ScopeRef  string
}

// MappingRow is a group_role_mappings row already filtered by the user's
// groups. Exported only so that the pure reduction can be tested.
type MappingRow struct {
	Role      Role
	ScopeType string
	ScopeRef  string
}

// ReduceSSOGrants turns the mapping rows that match a user's groups into the
// set of assignments to materialize: at most one global grant (the highest)
// and at most one per cluster (the highest), dropping the cluster grants that
// add nothing over the global one. A redundant cluster row would not change
// any permission, but it would make /api/users/{id}/access report the
// provenance as "both" and suggest that two things have to be revoked. Pure,
// tested. 'namespace' rows are ignored, as everywhere else.
func ReduceSSOGrants(rows []MappingRow) []SSOGrant {
	var global Role
	perCluster := map[string]Role{}
	var order []string
	for _, m := range rows {
		if !m.Role.Valid() {
			continue
		}
		switch m.ScopeType {
		case "global":
			if m.Role.rank() > global.rank() {
				global = m.Role
			}
		case "cluster":
			if m.ScopeRef == "" {
				continue
			}
			cur, seen := perCluster[m.ScopeRef]
			if !seen {
				order = append(order, m.ScopeRef)
			}
			if m.Role.rank() > cur.rank() {
				perCluster[m.ScopeRef] = m.Role
			}
		}
	}
	var out []SSOGrant
	if global != "" {
		out = append(out, SSOGrant{Role: global, ScopeType: "global"})
	}
	for _, ref := range order {
		if r := perCluster[ref]; r.rank() > global.rank() {
			out = append(out, SSOGrant{Role: r, ScopeType: "cluster", ScopeRef: ref})
		}
	}
	return out
}

// GrantsFromGroups resolves, from the user's identity provider group IDs
// (token claim or Microsoft Graph fallback), the assignments their
// group_role_mappings give them: global and per-cluster. An empty result is
// not an error here: the login decides, after syncing, whether a manual
// assignment remains. Mappings to a cluster that no longer exists are skipped
// (scope_ref is TEXT, not an FK, see ClusterHandler.Delete).
func GrantsFromGroups(ctx context.Context, pool *pgxpool.Pool, groupIDs []string) ([]SSOGrant, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx,
		`SELECT gm.role, gm.scope_type, COALESCE(gm.scope_ref, '')
		 FROM group_role_mappings gm
		 WHERE gm.ad_group_id = ANY($1)
		   AND (gm.scope_type = 'global'
		        OR (gm.scope_type = 'cluster' AND EXISTS (SELECT 1 FROM clusters c WHERE c.id::text = gm.scope_ref)))`,
		groupIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("rbac: failed querying group_role_mappings: %w", err)
	}
	defer rows.Close()
	var list []MappingRow
	for rows.Next() {
		var role, scopeType, scopeRef string
		if err := rows.Scan(&role, &scopeType, &scopeRef); err != nil {
			return nil, fmt.Errorf("rbac: failed reading group mapping: %w", err)
		}
		list = append(list, MappingRow{Role: Role(role), ScopeType: scopeType, ScopeRef: scopeRef})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rbac: failed iterating group mappings: %w", err)
	}
	return ReduceSSOGrants(list), nil
}

// SyncSSORoleAssignments makes the user's origin='sso' rows in
// role_assignments equal to grants: it deletes the ones their groups no
// longer give and inserts the missing ones. That way the rest of the system
// (EffectiveGlobalRole, EffectiveRoleForCluster, the whole authorization
// path) keeps reading only role_assignments without knowing anything about
// the IdP. Rows with origin='manual' are never touched: an admin's grant
// survives the user's groups, and when a manual row identical to a grant
// already exists no 'sso' twin is inserted (the unique indexes do not include
// origin, so there is a single row per user/role/scope).
//
// It returns keptLastAdmin=true when the user's groups no longer make them
// global admin but they are the last active one and their admin row came from
// SSO: that row is kept. Without the guard, changing the group mapping of the
// only global admin (or removing them from the group at the IdP) left the
// panel with nobody able to manage users or clusters on their next login —
// the same self-lockout UsersHandler.RevokeRole prevents through the UI. The
// caller must audit it.
func SyncSSORoleAssignments(ctx context.Context, pool *pgxpool.Pool, userID int64, grants []SSOGrant) (keptLastAdmin bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("rbac: failed opening transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := LockGlobalAdmins(ctx, tx); err != nil {
		return false, err
	}

	wantsGlobalAdmin := false
	for _, g := range grants {
		if g.ScopeType == "global" && g.Role == RoleAdmin {
			wantsGlobalAdmin = true
		}
	}
	keepSSOAdmin := false
	if !wantsGlobalAdmin {
		// Only matters if the user's ONLY global admin row is an 'sso' one
		// (a manual one is not going to be deleted anyway).
		var ssoAdmin, manualAdmin bool
		if err := tx.QueryRow(ctx,
			`SELECT
			   EXISTS (SELECT 1 FROM role_assignments WHERE user_id = $1 AND role = 'admin' AND scope_type = 'global' AND origin = 'sso'),
			   EXISTS (SELECT 1 FROM role_assignments WHERE user_id = $1 AND role = 'admin' AND scope_type = 'global' AND origin = 'manual')`,
			userID,
		).Scan(&ssoAdmin, &manualAdmin); err != nil {
			return false, fmt.Errorf("rbac: failed checking whether it is the last global admin: %w", err)
		}
		if ssoAdmin && !manualAdmin {
			others, err := CountActiveGlobalAdmins(ctx, tx, userID, 0)
			if err != nil {
				return false, err
			}
			keepSSOAdmin = others == 0
		}
	}

	roles := make([]string, 0, len(grants))
	scopeTypes := make([]string, 0, len(grants))
	scopeRefs := make([]string, 0, len(grants))
	for _, g := range grants {
		roles = append(roles, string(g.Role))
		scopeTypes = append(scopeTypes, g.ScopeType)
		scopeRefs = append(scopeRefs, g.ScopeRef)
	}

	// Delete the 'sso' rows that are not in the desired set (scope_ref NULL is
	// compared as '' on both sides).
	if _, err := tx.Exec(ctx,
		`DELETE FROM role_assignments ra
		 WHERE ra.user_id = $1 AND ra.origin = 'sso'
		   AND NOT EXISTS (
		     SELECT 1 FROM unnest($2::text[], $3::text[], $4::text[]) AS w(role, scope_type, scope_ref)
		     WHERE w.role = ra.role::text AND w.scope_type = ra.scope_type::text
		       AND w.scope_ref = COALESCE(ra.scope_ref, ''))
		   AND NOT ($5 AND ra.role = 'admin' AND ra.scope_type = 'global')`,
		userID, roles, scopeTypes, scopeRefs, keepSSOAdmin,
	); err != nil {
		return false, fmt.Errorf("rbac: failed clearing stale SSO assignments: %w", err)
	}

	for _, g := range grants {
		var ref *string
		if g.ScopeType != "global" {
			if _, err := strconv.ParseInt(g.ScopeRef, 10, 64); err != nil {
				continue // not a cluster id; GrantsFromGroups never yields this
			}
			r := g.ScopeRef
			ref = &r
		}
		// ON CONFLICT DO NOTHING without a target covers both unique
		// constraints (scope_ref set, and the partial index for NULL):
		// an identical row, manual or sso, is left as it is.
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_assignments (user_id, role, scope_type, scope_ref, origin)
			 VALUES ($1, $2, $3, $4, 'sso') ON CONFLICT DO NOTHING`,
			userID, string(g.Role), g.ScopeType, ref,
		); err != nil {
			return false, fmt.Errorf("rbac: failed inserting SSO assignment: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("rbac: failed committing transaction: %w", err)
	}
	return keepSSOAdmin, nil
}
