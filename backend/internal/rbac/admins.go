package rbac

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier abstracts what the global admin guards need so they can run
// interchangeably on a transaction (pgx.Tx) or on the pool. These checks ONLY
// make sense inside a transaction: the lock below is xact-scoped.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// globalAdminsLockKey serializes every operation capable of leaving the system
// without any global admin. Arbitrary value, it only has to be the same in
// every place that touches that invariant.
const globalAdminsLockKey int64 = 20260006

// LockGlobalAdmins must be the FIRST statement of any transaction that is going
// to check CountActiveGlobalAdmins and then delete/disable something.
//
// A SELECT ... FOR UPDATE on the affected row is NOT enough, and the comment
// claiming otherwise in RevokeRole was false: two transactions revoking or
// deleting TWO different admins lock different rows, neither sees the other's
// change, both count "another admin remains" and the system is left with none.
// A fixed-key advisory lock is chosen over a FOR UPDATE of the assignment set
// because: (a) it does not depend on the order in which each handler takes its
// row locks, so there are no deadlocks by order inversion; (b) it also works
// when the set is empty, where a FOR UPDATE locks nothing; (c) it also covers
// the read of users.disabled, which a lock on role_assignments would not.
func LockGlobalAdmins(ctx context.Context, q Querier) error {
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, globalAdminsLockKey); err != nil {
		return fmt.Errorf("rbac: failed taking the global admins lock: %w", err)
	}
	return nil
}

// CountActiveGlobalAdmins counts the NON-disabled users that have a global
// scope admin assignment, optionally excluding one user and/or one specific
// assignment (0 = exclude nothing).
//
// It counts distinct users, not assignments, and discards disabled ones: the
// real question is "is anyone left who can administer the panel?", and a
// disabled admin cannot. Always call after LockGlobalAdmins.
func CountActiveGlobalAdmins(ctx context.Context, q Querier, exceptUserID, exceptAssignmentID int64) (int, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT count(DISTINCT ra.user_id)
		 FROM role_assignments ra JOIN users u ON u.id = ra.user_id
		 WHERE ra.role = 'admin' AND ra.scope_type = 'global' AND NOT u.disabled
		   AND ($1 = 0 OR ra.user_id <> $1)
		   AND ($2 = 0 OR ra.id <> $2)`,
		exceptUserID, exceptAssignmentID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("rbac: failed counting active global admins: %w", err)
	}
	return n, nil
}
