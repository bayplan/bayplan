package middleware

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
)

// AuditDenied audits the mutating requests the middleware chain rejects on
// authorization grounds (403: insufficient role or invalid CSRF token). Without
// it, audit_log only held successful actions: a user probing administration
// endpoints left no trace at all.
//
// It must be mounted INSIDE RequireSession (to have the session in the context
// and be able to attribute the attempt) and OUTSIDE RequireCSRF/RequireAdmin/
// RequireClusterRole, which are the ones emitting the 403.
//
// It is deliberately NOT mounted on the WebSocket chains (logs, exec): there
// the 403 is emitted before the upgrade and wrapping the ResponseWriter on a
// route that hijacks adds nothing.
func AuditDenied(pool *pgxpool.Pool, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &deniedRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			if !shouldAudit(r.Method, rec.status) {
				return
			}

			detail := map[string]any{"method": r.Method, "path": r.URL.Path}
			// The body written by http.Error tells a possible CSRF ("invalid
			// CSRF token") apart from a privilege escalation ("admin role
			// required"): without it, both are the same line in the audit log.
			if reason := strings.TrimSpace(rec.body); reason != "" {
				detail["reason"] = reason
			}

			var actorID *int64
			actorUsername := ""
			if sess, ok := SessionFromContext(r.Context()); ok {
				actorID = &sess.UserID
				actorUsername = sess.Username
			}

			// WithoutCancel: if the client drops the connection, the request
			// context is already cancelled and the audit INSERT would fail —
			// that is, aborting would be enough to leave no trace of the
			// attempt.
			audit.Log(context.WithoutCancel(r.Context()), pool, logger, audit.Entry{
				UserID: actorID, ActorUsername: actorUsername, IP: ClientIP(r),
				Action: "access.denied", Result: audit.ResultDenied, Detail: detail,
			})
		})
	}
}

// shouldAudit decides which responses are an authorization denial that
// deserves an audit line.
//
// Only 403, and only on mutating methods. In particular NOT the 409s: there are
// 409s that are data validation, not denials (duplicate username or cluster
// name), and auditing them as "access.denied" would pollute exactly the signal
// we are after. The 409s of the security guards are audited by each handler
// with its own action and reason. Nor the 401: it is expired-session noise, and
// login is already audited separately.
func shouldAudit(method string, status int) bool {
	return IsMutating(method) && status == http.StatusForbidden
}

// IsMutating tells apart the requests that may change state. Shared with
// RequireCSRF and with RequireSession's denial hook so that "what is mutating"
// is decided in a single place.
func IsMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// deniedRecorder captures the status and the (short, http.Error) body without
// breaking Hijacker/Flusher — same approach as statusRecorder in
// internal/metrics.
type deniedRecorder struct {
	http.ResponseWriter
	status int
	body   string
}

// maxReasonLen bounds how much of the body is kept: http.Error messages are
// one line, and a handler that writes a large response with a 403 has no
// business ending up whole inside audit_log.detail.
const maxReasonLen = 200

func (d *deniedRecorder) WriteHeader(status int) {
	d.status = status
	d.ResponseWriter.WriteHeader(status)
}

func (d *deniedRecorder) Write(b []byte) (int, error) {
	if d.status == http.StatusForbidden && len(d.body) < maxReasonLen {
		d.body += string(b)
		if len(d.body) > maxReasonLen {
			d.body = d.body[:maxReasonLen]
		}
	}
	return d.ResponseWriter.Write(b)
}

func (d *deniedRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := d.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("middleware: the underlying ResponseWriter does not support Hijack")
	}
	return hj.Hijack()
}

func (d *deniedRecorder) Flush() {
	if f, ok := d.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
