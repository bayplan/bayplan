package api

import (
	"net/http"
	"strconv"

	"github.com/bayplan/bayplan/internal/rbac"
)

type accessResponse struct {
	UserID     int64                `json:"userId"`
	Username   string               `json:"username"`
	GlobalRole string               `json:"globalRole,omitempty"`
	Clusters   []rbac.ClusterAccess `json:"clusters"`
}

// Access returns a user's EFFECTIVE permissions: which role they really have on
// each cluster and where it comes from.
//
// It exists because /api/users only returns the raw role_assignments rows, and
// what someone can actually do cannot be read from those: a user with global
// readonly and admin on one cluster shows up as two loose rows that have to be
// combined mentally by applying a rule that lives somewhere else.
//
// The resolution is done by rbac.EffectiveAccess, the SAME rule that
// EffectiveRoleForCluster applies on every request. Not having a second
// implementation here is deliberate: a permissions screen that disagrees with
// the middleware is worse than having no screen at all.
func (h *UsersHandler) Access(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var username string
	if err := h.DB.QueryRow(r.Context(), `SELECT username FROM users WHERE id = $1`, userID).Scan(&username); err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	assignments, err := h.userAssignments(r, userID)
	if err != nil {
		h.Logger.Error("users.access: failed to query assignments", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	clusters, err := h.allClusters(r)
	if err != nil {
		h.Logger.Error("users.access: failed to query clusters", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	globalRole, access := rbac.EffectiveAccess(assignments, clusters)
	writeJSON(w, http.StatusOK, accessResponse{
		UserID: userID, Username: username,
		GlobalRole: string(globalRole), Clusters: access,
	})
}

func (h *UsersHandler) userAssignments(r *http.Request, userID int64) ([]rbac.Assignment, error) {
	rows, err := h.DB.Query(r.Context(),
		`SELECT role, scope_type, COALESCE(scope_ref, '') FROM role_assignments WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []rbac.Assignment
	for rows.Next() {
		var a rbac.Assignment
		if err := rows.Scan(&a.Role, &a.ScopeType, &a.ScopeRef); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// allClusters lists ALL clusters, disabled ones included: the matrix has to
// make visible that someone keeps permissions on a switched-off cluster, which
// is precisely what gets forgotten when it is enabled again.
func (h *UsersHandler) allClusters(r *http.Request) ([]rbac.ClusterRef, error) {
	rows, err := h.DB.Query(r.Context(), `SELECT id, display_name FROM clusters`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []rbac.ClusterRef
	for rows.Next() {
		var c rbac.ClusterRef
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
