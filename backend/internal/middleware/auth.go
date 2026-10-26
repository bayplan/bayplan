// Package middleware applies authentication, authorization, CSRF and rate
// limiting to the HTTP handlers.
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/bayplan/bayplan/internal/clientip"
	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/bayplan/bayplan/internal/session"
)

type ctxKey int

const (
	ctxKeySession ctxKey = iota
	ctxKeyRole
)

// RoleResolver resolves a user's effective role (see rbac.EffectiveGlobalRole).
type RoleResolver func(ctx context.Context, userID int64) (rbac.Role, error)

// DeniedHook receives the denials RequireSession emits by itself, so that the
// router can audit them. It exists because this 403 happens BEFORE AuditDenied
// in the chain and it is not noise: it is emitted for a user who has logged in
// correctly (local login does not require a role) but has no assignment at all
// — a fully-fledged authorization denial. It may be nil.
type DeniedHook func(r *http.Request, sess *session.Session, reason string)

// RequireSession requires a valid session (401 otherwise), resolves the user's
// role (403 if none is assigned) and injects both into the request context so
// that handlers do not repeat queries.
func RequireSession(mgr *session.Manager, resolveRole RoleResolver, onDenied DeniedHook) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, err := mgr.Validate(r.Context(), r)
			if err != nil {
				// The 401 is deliberately not audited: it is noise from an
				// expired or revoked session (including a just-disabled user),
				// and login is already audited on its own.
				http.Error(w, "not authenticated", http.StatusUnauthorized)
				return
			}
			// A pending forced password change is enforced HERE, not only by
			// the frontend's redirect: otherwise the bootstrap password (which
			// the operator may have left in a values file or a shell history)
			// would keep working against the whole API.
			if sess.MustChangePassword && !AllowedWhilePasswordChangePending(r.Method, r.URL.Path) {
				http.Error(w, "password change required", http.StatusForbidden)
				return
			}
			role, err := resolveRole(r.Context(), sess.UserID)
			if err != nil {
				if !errors.Is(err, rbac.ErrNoRole) {
					// A DB failure is not a denial: neither a 403 nor an
					// access.denied entry that would blame the user.
					slog.Error("auth: failed resolving role", "error", err, "userId", sess.UserID)
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				if onDenied != nil {
					onDenied(r, sess, "no permissions assigned")
				}
				http.Error(w, "no permissions assigned", http.StatusForbidden)
				return
			}
			role = capRole(sess, role)
			ctx := context.WithValue(r.Context(), ctxKeySession, sess)
			ctx = context.WithValue(ctx, ctxKeyRole, role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AllowedWhilePasswordChangePending lists what a session with
// must_change_password may still call: what the change-password page needs to
// render (who am I, the language/theme preferences the shell writes), the
// change itself and logout. Everything else, API token creation included,
// answers 403 until the password is changed. Pure, tested.
func AllowedWhilePasswordChangePending(method, path string) bool {
	switch method + " " + path {
	case "GET /api/auth/me",
		"POST /api/auth/change-password",
		"POST /api/auth/logout",
		"PATCH /api/auth/preferences":
		return true
	}
	return false
}

// RequireAdmin requires the GLOBAL admin role. It must be mounted after
// RequireSession. Used for actions that do not belong to a specific cluster
// (cluster management, user/role management): a 'cluster'-scoped admin is
// deliberately not enough here.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, ok := RoleFromContext(r.Context())
		if !ok || !role.IsAdmin() {
			http.Error(w, "admin role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClusterRoleResolver resolves a user's effective role on a specific cluster
// (see rbac.EffectiveRoleForCluster).
type ClusterRoleResolver func(ctx context.Context, userID, clusterID int64) (rbac.Role, error)

// RequireClusterRole requires the session user to hold at least the `min`
// role on the cluster identified by the "id" path value (global assignment,
// or a 'cluster'-scoped one for that specific id). It must be mounted after
// RequireSession, whose session already injected in the context it reuses: it
// does not resolve the global role again, it resolves a new one specific to
// this cluster.
func RequireClusterRole(resolve ClusterRoleResolver, min rbac.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, ok := SessionFromContext(r.Context())
			if !ok {
				http.Error(w, "not authenticated", http.StatusUnauthorized)
				return
			}
			clusterID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
			if err != nil {
				http.Error(w, "invalid cluster id", http.StatusBadRequest)
				return
			}
			role, err := resolve(r.Context(), sess.UserID, clusterID)
			if err != nil && !errors.Is(err, rbac.ErrNoRole) {
				// Not audited as access.denied by AuditDenied (it only records
				// 403s): a DB failure says nothing about the user's permissions.
				slog.Error("auth: failed resolving cluster role", "error", err, "userId", sess.UserID, "clusterId", clusterID)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if err == nil {
				role = capRole(sess, role)
			}
			if err != nil || !role.AtLeast(min) {
				http.Error(w, "no permissions on this cluster", http.StatusForbidden)
				return
			}
			// The effective role on THIS cluster (already capped) replaces the
			// global one in the context: handlers pick the impersonated group
			// with it.
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRole, role)))
		})
	}
}

// CapRoleFromContext applies the read-only token cap to a role a handler has
// resolved on its own (e.g. to decide whether to show a Secret's values).
// Without a session in the context it returns the role as-is.
func CapRoleFromContext(ctx context.Context, role rbac.Role) rbac.Role {
	if sess, ok := SessionFromContext(ctx); ok {
		return capRole(sess, role)
	}
	return role
}

// capRole applies the cap of a read-only API token. It is done in the
// middleware, a single place, so that no handler has to remember it.
func capRole(sess *session.Session, role rbac.Role) rbac.Role {
	if sess.ReadOnly && role.AtLeast(rbac.RoleEditor) {
		return rbac.RoleReadonly
	}
	return role
}

func SessionFromContext(ctx context.Context) (*session.Session, bool) {
	s, ok := ctx.Value(ctxKeySession).(*session.Session)
	return s, ok
}

func RoleFromContext(ctx context.Context) (rbac.Role, bool) {
	r, ok := ctx.Value(ctxKeyRole).(rbac.Role)
	return r, ok
}

// ClientIP extracts the request's source IP (for auditing and rate limiting).
// X-Forwarded-For is honored only if the request comes from a proxy declared
// in TRUSTED_PROXIES (see internal/clientip).
func ClientIP(r *http.Request) string {
	return clientip.FromRequest(r)
}
