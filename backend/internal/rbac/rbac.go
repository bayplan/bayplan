// Package rbac resolves a user's effective internal role. Authentication (who
// you are, in auth/session) and authorization (what you can do, here) are
// decoupled on purpose: both local login and the future Entra ID SSO (Phase 2)
// resolve to the same permission model.
package rbac

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleEditor   Role = "editor"
	RoleReadonly Role = "readonly"
)

// rank is the ONLY place that defines the privilege order: admin > editor >
// readonly > "" (no role). The Postgres enum carries no semantic order.
func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleEditor:
		return 2
	case RoleReadonly:
		return 1
	}
	return 0
}

// Valid says whether r is one of the known roles.
func (r Role) Valid() bool { return r.rank() > 0 }

func (r Role) IsAdmin() bool { return r == RoleAdmin }

// CanWrite says whether the role can modify workloads (restart, scale, apply
// YAML, cordon). Deleting, opening a terminal, draining nodes and reading
// secret values remain admin-only.
func (r Role) CanWrite() bool { return r.rank() >= RoleEditor.rank() }

// AtLeast says whether r satisfies the required minimum min.
func (r Role) AtLeast(min Role) bool {
	return r.rank() >= min.rank()
}

// ErrNoRole means the user has no role assigned: it must not be treated as
// "readonly by default", but as access denied.
var ErrNoRole = errors.New("rbac: the user has no role assigned")

// highest reduces a list of roles to the most privileged one. Kept apart from
// the SQL queries on purpose: when an additional source of candidates comes
// into play (e.g. group_role_mappings resolved via SSO), it is enough to join
// the rows before calling this function, without touching the reduction logic.
func highest(roles []Role) (Role, bool) {
	var best Role
	for _, r := range roles {
		if r.rank() > best.rank() {
			best = r
		}
	}
	return best, best != ""
}

// EffectiveGlobalRole returns the most privileged role assigned to the user
// with scope_type='global'.
func EffectiveGlobalRole(ctx context.Context, pool *pgxpool.Pool, userID int64) (Role, error) {
	rows, err := pool.Query(ctx,
		`SELECT role FROM role_assignments WHERE user_id = $1 AND scope_type = 'global'`,
		userID,
	)
	if err != nil {
		return "", fmt.Errorf("rbac: failed querying roles: %w", err)
	}
	defer rows.Close()
	roles, err := scanRoles(rows)
	if err != nil {
		return "", err
	}
	role, ok := highest(roles)
	if !ok {
		return "", ErrNoRole
	}
	return role, nil
}

// EffectiveRoleForCluster returns the user's most privileged role that applies
// to clusterID: global assignments (apply to every cluster) or 'cluster' scope
// assignments whose scope_ref matches this cluster. The 'namespace' scope
// exists in the schema but is not resolved here — see the note in the plan:
// really enabling it requires filtering every informer cache read by allowed
// namespace, out of scope for now.
func EffectiveRoleForCluster(ctx context.Context, pool *pgxpool.Pool, userID, clusterID int64) (Role, error) {
	rows, err := pool.Query(ctx,
		`SELECT role FROM role_assignments
		 WHERE user_id = $1
		   AND (scope_type = 'global' OR (scope_type = 'cluster' AND scope_ref = $2))`,
		userID, strconv.FormatInt(clusterID, 10),
	)
	if err != nil {
		return "", fmt.Errorf("rbac: failed querying cluster roles: %w", err)
	}
	defer rows.Close()
	roles, err := scanRoles(rows)
	if err != nil {
		return "", err
	}
	role, ok := highest(roles)
	if !ok {
		return "", ErrNoRole
	}
	return role, nil
}

// HasAnyAssignment says whether the user has at least one row in
// role_assignments, regardless of scope. Used so that login does not block a
// user who only has 'cluster' scope assignments (no global role):
// RequireSession must only return ErrNoRole if the user has no assignment at
// all.
func HasAnyAssignment(ctx context.Context, pool *pgxpool.Pool, userID int64) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM role_assignments WHERE user_id = $1)`,
		userID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("rbac: failed checking assignments: %w", err)
	}
	return exists, nil
}

func scanRoles(rows pgx.Rows) ([]Role, error) {
	var roles []Role
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("rbac: failed reading role: %w", err)
		}
		roles = append(roles, Role(r))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rbac: failed iterating roles: %w", err)
	}
	return roles, nil
}
