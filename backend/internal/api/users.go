package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/auth"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/bayplan/bayplan/internal/session"
)

// UsersHandler manages the role assignments (role_assignments) of users that
// already exist in the users table — it is not a directory of all of AD/Entra,
// it only manages whoever has signed in at least once (local or SSO). All
// admin-only (global): granting/revoking permissions is a platform action, not
// one of a specific cluster.
type UsersHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
}

type roleAssignmentResponse struct {
	ID          int64  `json:"id"`
	Role        string `json:"role"`
	ScopeType   string `json:"scopeType"`
	ScopeRef    string `json:"scopeRef,omitempty"`
	ClusterName string `json:"clusterName,omitempty"`
	// Origin: "manual" (granted by an admin) or "sso" (derived from the
	// user's IdP groups on their last login; revoking it only lasts until the
	// next login, the group mapping is what has to change).
	Origin string `json:"origin"`
}

type userWithRolesResponse struct {
	ID       int64                    `json:"id"`
	Username string                   `json:"username"`
	Source   string                   `json:"source"`
	Disabled bool                     `json:"disabled"`
	Roles    []roleAssignmentResponse `json:"roles"`
}

// List returns every user with their current role assignments.
func (h *UsersHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `SELECT id, username, source, disabled FROM users ORDER BY username`)
	if err != nil {
		h.Logger.Error("users.list: failed querying users", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byID := map[int64]*userWithRolesResponse{}
	order := []int64{}
	for rows.Next() {
		var u userWithRolesResponse
		if err := rows.Scan(&u.ID, &u.Username, &u.Source, &u.Disabled); err != nil {
			rows.Close()
			h.Logger.Error("users.list: failed reading user", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		u.Roles = []roleAssignmentResponse{}
		byID[u.ID] = &u
		order = append(order, u.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		h.Logger.Error("users.list: failed iterating users", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	raRows, err := h.DB.Query(r.Context(), `
		SELECT ra.id, ra.user_id, ra.role, ra.scope_type, ra.scope_ref, c.display_name, ra.origin
		FROM role_assignments ra
		LEFT JOIN clusters c ON ra.scope_type = 'cluster' AND c.id::text = ra.scope_ref
		ORDER BY ra.id`)
	if err != nil {
		h.Logger.Error("users.list: failed querying assignments", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer raRows.Close()
	for raRows.Next() {
		var ra roleAssignmentResponse
		var userID int64
		var scopeRef, clusterName *string
		if err := raRows.Scan(&ra.ID, &userID, &ra.Role, &ra.ScopeType, &scopeRef, &clusterName, &ra.Origin); err != nil {
			h.Logger.Error("users.list: failed reading assignment", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if scopeRef != nil {
			ra.ScopeRef = *scopeRef
		}
		if clusterName != nil {
			ra.ClusterName = *clusterName
		}
		if u, ok := byID[userID]; ok {
			u.Roles = append(u.Roles, ra)
		}
	}
	if err := raRows.Err(); err != nil {
		h.Logger.Error("users.list: failed iterating assignments", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	out := make([]*userWithRolesResponse, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	writeJSON(w, http.StatusOK, out)
}

// Create registers a new local user. The admin writes the definitive password
// directly (product decision: unlike the initial bootstrap, no random one is
// generated here and no change is forced on first login) — as in the rest of
// the local flow, it is validated with the same strength policy and only the
// bcrypt hash is stored. The user is left without any role_assignment: they
// will not be able to do anything until a role is granted with GrantRole, on
// the same screen.
func (h *UsersHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if err := auth.ValidateUsername(body.Username); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := auth.ValidatePasswordStrength(body.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		h.Logger.Error("users.create: failed generating hash", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var userID int64
	err = h.DB.QueryRow(r.Context(),
		`INSERT INTO users (username, source, password_hash, must_change_password)
		 VALUES ($1, 'local', $2, FALSE) RETURNING id`,
		body.Username, hash,
	).Scan(&userID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation (username)
			http.Error(w, "a user with that name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("users.create: failed inserting user", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sess, _ := middleware.SessionFromContext(r.Context())
	var actorID *int64
	if sess != nil {
		actorID = &sess.UserID
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: actorID, IP: middleware.ClientIP(r), Action: "user.create", Result: audit.ResultSuccess,
		Detail: map[string]any{"targetUserId": userID, "username": body.Username},
	})

	writeJSON(w, http.StatusCreated, userWithRolesResponse{
		ID: userID, Username: body.Username, Source: "local", Roles: []roleAssignmentResponse{},
	})
}

// GrantRole grants a role to a user in a given scope. It explicitly rejects
// scope 'namespace': the schema allows it but no read applies it yet (see
// internal/k8s/read.go) — granting that scope would be a permission that
// appears to restrict without really restricting anything.
func (h *UsersHandler) GrantRole(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	var body struct {
		Role      string `json:"role"`
		ScopeType string `json:"scopeType"`
		ScopeRef  string `json:"scopeRef"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	role := rbac.Role(body.Role)
	if !role.Valid() {
		http.Error(w, "role must be 'admin', 'editor' or 'readonly'", http.StatusBadRequest)
		return
	}

	var scopeRef *string
	switch body.ScopeType {
	case "global":
		if body.ScopeRef != "" {
			http.Error(w, "scopeRef must be empty for scope 'global'", http.StatusBadRequest)
			return
		}
	case "cluster":
		if body.ScopeRef == "" {
			http.Error(w, "scopeRef (cluster id) is required for scope 'cluster'", http.StatusBadRequest)
			return
		}
		clusterID, err := strconv.ParseInt(body.ScopeRef, 10, 64)
		if err != nil {
			http.Error(w, "scopeRef must be a numeric cluster id", http.StatusBadRequest)
			return
		}
		var exists bool
		if err := h.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM clusters WHERE id = $1)`, clusterID).Scan(&exists); err != nil {
			h.Logger.Error("users.grantRole: failed checking cluster", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, "the specified cluster does not exist", http.StatusUnprocessableEntity)
			return
		}
		scopeRef = &body.ScopeRef
	case "namespace":
		http.Error(w, "scope 'namespace' is not supported yet (no read applies it)", http.StatusUnprocessableEntity)
		return
	default:
		http.Error(w, "scopeType must be 'global' or 'cluster'", http.StatusBadRequest)
		return
	}

	// ON CONFLICT DO NOTHING without a target covers both the UNIQUE of 0001
	// (cluster scope) and the partial unique index over scope_ref NULL of 0030
	// (global scope, which the original UNIQUE never deduplicated: every NULL is
	// distinct). The old SELECT-then-INSERT was not atomic and let concurrent
	// twins through. Granting what already exists stays idempotent (201).
	tag, err := h.DB.Exec(r.Context(),
		`INSERT INTO role_assignments (user_id, role, scope_type, scope_ref, origin)
		 VALUES ($1, $2, $3, $4, 'manual') ON CONFLICT DO NOTHING`,
		userID, string(role), body.ScopeType, scopeRef,
	)
	if err != nil {
		// The user may have been deleted between the UI loading the list and
		// this submit (now that deletion exists): it is a 422, not a 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation (user_id)
			http.Error(w, "the specified user no longer exists", http.StatusUnprocessableEntity)
			return
		}
		h.Logger.Error("users.grantRole: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		// The identical row exists. If the SSO login put it there, an admin
		// granting it explicitly turns it into a manual grant: otherwise it
		// would silently disappear the next time the user's groups stop giving
		// it, which is not what "grant" means.
		if _, err := h.DB.Exec(r.Context(),
			`UPDATE role_assignments SET origin = 'manual'
			 WHERE user_id = $1 AND role = $2 AND scope_type = $3
			   AND scope_ref IS NOT DISTINCT FROM $4 AND origin = 'sso'`,
			userID, string(role), body.ScopeType, scopeRef,
		); err != nil {
			h.Logger.Error("users.grantRole: failed promoting SSO assignment", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	h.auditRole(r, "rbac.grant", userID, string(role), body.ScopeType, body.ScopeRef)
	w.WriteHeader(http.StatusCreated)
}

// RevokeRole removes a specific role assignment from a user. It explicitly
// blocks leaving the system without any global admin: without this check, an
// admin can revoke their own (or someone else's) only global admin assignment
// and leave the whole panel with nobody able to manage users or clusters — a
// real lockout, already observed in manual tests of this very endpoint.
func (h *UsersHandler) RevokeRole(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	assignmentID, err := strconv.ParseInt(r.PathValue("assignmentId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid assignment id", http.StatusBadRequest)
		return
	}

	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		h.Logger.Error("users.revokeRole: failed opening transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	// Before any FOR UPDATE: if the row lock were taken first, two crossed
	// revocations would deadlock (see rbac.LockGlobalAdmins).
	if err := rbac.LockGlobalAdmins(r.Context(), tx); err != nil {
		h.Logger.Error("users.revokeRole: failed taking lock", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var role, scopeType string
	var scopeRef *string
	err = tx.QueryRow(r.Context(),
		`SELECT role, scope_type, scope_ref FROM role_assignments WHERE id = $1 AND user_id = $2 FOR UPDATE`,
		assignmentID, userID,
	).Scan(&role, &scopeType, &scopeRef)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "assignment not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("users.revokeRole: failed querying assignment", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if role == string(rbac.RoleAdmin) && scopeType == "global" {
		otherGlobalAdmins, err := rbac.CountActiveGlobalAdmins(r.Context(), tx, 0, assignmentID)
		if err != nil {
			h.Logger.Error("users.revokeRole: failed checking other global admins", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if otherGlobalAdmins == 0 {
			h.auditUserDenied(r, "rbac.revoke", userID, "", "last_admin")
			http.Error(w, "cannot revoke: it is the system's last global admin assignment", http.StatusConflict)
			return
		}
	}

	if _, err := tx.Exec(r.Context(), `DELETE FROM role_assignments WHERE id = $1`, assignmentID); err != nil {
		h.Logger.Error("users.revokeRole: delete failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.Logger.Error("users.revokeRole: failed committing transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ref := ""
	if scopeRef != nil {
		ref = *scopeRef
	}
	h.auditRole(r, "rbac.revoke", userID, role, scopeType, ref)
	w.WriteHeader(http.StatusNoContent)
}

// Delete removes a local user from the system. It is a PHYSICAL deletion:
// their sessions, role assignments and bootstrap tokens fall through ON DELETE
// CASCADE, and their audit_log rows are preserved intact (audit_log.user_id no
// longer has an FK, see migration 0006 — which is why this is now possible).
//
// Careful, same caveat as ClusterHandler.Delete: the logs/exec WebSockets the
// user already had open are NOT cut off, authorization is only checked at the
// handshake.
func (h *UsersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	sess, _ := middleware.SessionFromContext(r.Context())
	if sess != nil && sess.UserID == userID {
		h.auditUserDenied(r, "user.delete", userID, "", "self")
		http.Error(w, "you cannot delete your own user", http.StatusConflict)
		return
	}

	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		h.Logger.Error("users.delete: failed opening transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	if err := rbac.LockGlobalAdmins(r.Context(), tx); err != nil {
		h.Logger.Error("users.delete: failed taking lock", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var username, source string
	err = tx.QueryRow(r.Context(),
		`SELECT username, source FROM users WHERE id = $1 FOR UPDATE`, userID,
	).Scan(&username, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("users.delete: failed querying user", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// An 'oidc' user is re-created on their own at their next SSO login
	// (upsertSSOUser), so deleting them here would be smoke: to cut off their
	// access you have to remove the group mapping or disable them.
	if source != "local" {
		h.auditUserDenied(r, "user.delete", userID, username, "not_local")
		http.Error(w, "only local users can be deleted; disable the user instead", http.StatusConflict)
		return
	}

	otherAdmins, err := rbac.CountActiveGlobalAdmins(r.Context(), tx, userID, 0)
	if err != nil {
		h.Logger.Error("users.delete: failed checking other global admins", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if otherAdmins == 0 {
		h.auditUserDenied(r, "user.delete", userID, username, "last_admin")
		http.Error(w, "cannot delete: it is the system's last active global admin", http.StatusConflict)
		return
	}

	if _, err := tx.Exec(r.Context(), `DELETE FROM users WHERE id = $1`, userID); err != nil {
		h.Logger.Error("users.delete: delete failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.Logger.Error("users.delete: failed committing transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.auditUser(r, "user.delete", audit.ResultSuccess, userID, username, nil)
	w.WriteHeader(http.StatusNoContent)
}

// SetDisabled disables or re-enables a user. Disabling is the reversible
// removal (the user and their history are kept) and revokes their sessions
// instantly; session.Validate also checks users.disabled on every request.
func (h *UsersHandler) SetDisabled(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	// Pointer and not bool: with a bool, a {} body or a misspelled field would
	// yield false as the zero value and silently RE-ENABLE the user, which is
	// precisely the branch without guards.
	var body struct {
		Disabled *bool `json:"disabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Disabled == nil {
		http.Error(w, "disabled is required (true or false)", http.StatusBadRequest)
		return
	}
	disabled := *body.Disabled

	sess, _ := middleware.SessionFromContext(r.Context())
	if disabled && sess != nil && sess.UserID == userID {
		h.auditUserDenied(r, "user.disable", userID, "", "self")
		http.Error(w, "you cannot disable your own user", http.StatusConflict)
		return
	}

	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		h.Logger.Error("users.setDisabled: failed opening transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	if err := rbac.LockGlobalAdmins(r.Context(), tx); err != nil {
		h.Logger.Error("users.setDisabled: failed taking lock", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var username string
	err = tx.QueryRow(r.Context(), `SELECT username FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&username)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("users.setDisabled: failed querying user", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if disabled {
		otherAdmins, err := rbac.CountActiveGlobalAdmins(r.Context(), tx, userID, 0)
		if err != nil {
			h.Logger.Error("users.setDisabled: failed checking other global admins", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if otherAdmins == 0 {
			h.auditUserDenied(r, "user.disable", userID, username, "last_admin")
			http.Error(w, "cannot disable: it is the system's last active global admin", http.StatusConflict)
			return
		}
	}

	if _, err := tx.Exec(r.Context(),
		`UPDATE users SET disabled = $1, updated_at = now() WHERE id = $2`, disabled, userID,
	); err != nil {
		h.Logger.Error("users.setDisabled: update failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.Logger.Error("users.setDisabled: failed committing transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	action := "user.enable"
	if disabled {
		action = "user.disable"
		// session.Validate would already reject their requests, but leaving the
		// rows explicitly revoked makes the cutoff visible in the sessions table.
		if err := session.RevokeAllForUser(r.Context(), h.DB, userID); err != nil {
			h.Logger.Error("users.setDisabled: failed revoking sessions", "error", err)
		}
	}
	h.auditUser(r, action, audit.ResultSuccess, userID, username, nil)
	w.WriteHeader(http.StatusNoContent)
}

// auditUser records an action on a specific user. extra allows adding fields
// to the detail without duplicating the construction of the entry.
func (h *UsersHandler) auditUser(r *http.Request, action string, result audit.Result, targetUserID int64, targetUsername string, extra map[string]any) {
	sess, _ := middleware.SessionFromContext(r.Context())
	var actorID *int64
	actorUsername := ""
	if sess != nil {
		actorID = &sess.UserID
		actorUsername = sess.Username
	}
	detail := map[string]any{"targetUserId": targetUserID}
	if targetUsername != "" {
		detail["username"] = targetUsername
	}
	for k, v := range extra {
		detail[k] = v
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: actorID, ActorUsername: actorUsername, IP: middleware.ClientIP(r),
		Action: action, Result: result, Detail: detail,
	})
}

// auditUserDenied leaves a trace of the guards' rejections (409).
// middleware.AuditDenied does not cover them on purpose: it only audits 403,
// because there are 409s that are data validation (duplicate name) and not
// denials.
func (h *UsersHandler) auditUserDenied(r *http.Request, action string, targetUserID int64, targetUsername, reason string) {
	h.auditUser(r, action, audit.ResultDenied, targetUserID, targetUsername, map[string]any{"reason": reason})
}

func (h *UsersHandler) auditRole(r *http.Request, action string, targetUserID int64, role, scopeType, scopeRef string) {
	sess, _ := middleware.SessionFromContext(r.Context())
	var actorID *int64
	actorUsername := ""
	if sess != nil {
		actorID = &sess.UserID
		actorUsername = sess.Username
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: actorID, ActorUsername: actorUsername, IP: middleware.ClientIP(r), Action: action, Result: audit.ResultSuccess,
		Detail: map[string]any{
			"targetUserId": targetUserID, "role": role, "scopeType": scopeType, "scopeRef": scopeRef,
		},
	})
}
