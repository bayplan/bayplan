// Package audit records every mutating action and every login attempt in
// audit_log. The table is append-only at the database level (see migration
// 0001, trigger trg_audit_log_immutable): neither a bug nor a compromised app
// can alter the history through the normal path.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Result string

const (
	ResultSuccess Result = "success"
	ResultFailure Result = "failure"
	ResultDenied  Result = "denied"
)

// Entry describes one audit line. UserID is nil when the actor is the system
// itself (e.g. a startup failure) or a failed login with no resolved user.
//
// ActorUsername is optional: fill it in when the username is already in memory
// (session.Session.Username). If empty, Log resolves it from user_id. It is
// stored denormalized because audit_log.user_id NO LONGER has an FK to users
// (migration 0006, needed to be able to delete users): without this field, the
// audit trail of a deleted user would be attributed to an orphan id.
type Entry struct {
	UserID        *int64
	ActorUsername string
	IP            string
	Action        string
	Cluster       string
	Namespace     string
	Resource      string
	Result        Result
	Detail        map[string]any
}

// Log inserts the audit entry. A failure writing the audit trail must not take
// down the request that produced it: it is logged and execution continues.
type sourceKey struct{}

// WithSource marks every entry logged under ctx as initiated from another
// channel than the UI or a script, e.g. "mcp" for an AI client. The handlers
// that audit do not need to know: the MCP server dispatches to them with this
// context and the mark ends up in detail.via.
func WithSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

// Source returns the mark WithSource put on ctx, if any.
func Source(ctx context.Context) string {
	source, _ := ctx.Value(sourceKey{}).(string)
	return source
}

func Log(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, e Entry) {
	if source := Source(ctx); source != "" {
		// Copied, not written into: callers sometimes reuse the map.
		withSource := make(map[string]any, len(e.Detail)+1)
		for k, v := range e.Detail {
			withSource[k] = v
		}
		withSource["via"] = source
		e.Detail = withSource
	}
	var detail []byte
	if e.Detail != nil {
		var err error
		detail, err = json.Marshal(e.Detail)
		if err != nil {
			logger.Error("audit: failed to serialize detail", "error", err, "action", e.Action)
		}
	}

	_, err := pool.Exec(ctx,
		`INSERT INTO audit_log (user_id, actor_username, ip, action, cluster, namespace, resource, result, detail)
		 VALUES ($1, COALESCE(NULLIF($2, ''), (SELECT username FROM users WHERE id = $1)),
		         NULLIF($3, '')::inet, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), $8, $9)`,
		e.UserID, e.ActorUsername, e.IP, e.Action, e.Cluster, e.Namespace, e.Resource, string(e.Result), detail,
	)
	if err != nil {
		logger.Error("audit: failed to insert audit entry", "error", err, "action", e.Action)
	}
}
