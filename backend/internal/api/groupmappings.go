package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/rbac"
)

// maxADGroupIDLen bounds the group identifier. It is normally the Object ID (a
// 36-character GUID), but Azure can be configured to emit in the `groups` claim
// the sAMAccountName or the qualified name of a group synchronised from
// on-prem, so the format is NOT validated: rejecting anything that is not a
// GUID would leave legitimate configurations out.
const maxADGroupIDLen = 256

// GroupMappingHandler manages group_role_mappings, the table that translates
// membership of an Entra ID group into a Bayplan role.
//
// Until now this table had NEITHER UI NOR ENDPOINT and was edited by hand via
// psql, while being the only source of roles for SSO: without at least one row
// covering one of the user's groups, EVERY SSO login fails with `no_role_mapped`
// even though the Azure credentials are correct.
type GroupMappingHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
}

type groupMappingResponse struct {
	ID        int64  `json:"id"`
	ADGroupID string `json:"adGroupId"`
	Role      string `json:"role"`
	ScopeType string `json:"scopeType"`
	ScopeRef  string `json:"scopeRef,omitempty"`
	// ClusterName resolves scope_ref to something readable. It may be empty even
	// with a scopeRef: scope_ref is TEXT without an FK, so it can end up pointing
	// at a cluster that no longer exists.
	ClusterName string    `json:"clusterName,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (h *GroupMappingHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(),
		`SELECT g.id, g.ad_group_id, g.role, g.scope_type, COALESCE(g.scope_ref, ''), COALESCE(c.display_name, ''), g.created_at
		 FROM group_role_mappings g
		 LEFT JOIN clusters c ON g.scope_type = 'cluster' AND g.scope_ref = c.id::text
		 ORDER BY g.ad_group_id, g.role`,
	)
	if err != nil {
		h.Logger.Error("groupmappings.list: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []groupMappingResponse{}
	for rows.Next() {
		var m groupMappingResponse
		if err := rows.Scan(&m.ID, &m.ADGroupID, &m.Role, &m.ScopeType, &m.ScopeRef, &m.ClusterName, &m.CreatedAt); err != nil {
			h.Logger.Error("groupmappings.list: failed to read row", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		h.Logger.Error("groupmappings.list: iteration failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *GroupMappingHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ADGroupID string `json:"adGroupId"`
		Role      string `json:"role"`
		ScopeType string `json:"scopeType"`
		ScopeRef  string `json:"scopeRef"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Trimmed: a group id with trailing spaces would never match the token
	// claim and the failure would be invisible (login denied with no apparent
	// reason).
	adGroupID := strings.TrimSpace(body.ADGroupID)
	if adGroupID == "" || len(adGroupID) > maxADGroupIDLen {
		http.Error(w, "adGroupId is required", http.StatusBadRequest)
		return
	}

	role := rbac.Role(body.Role)
	if !role.Valid() {
		http.Error(w, "role must be 'admin', 'editor' or 'readonly'", http.StatusBadRequest)
		return
	}

	scopeRef, ok := h.resolveScope(w, r, body.ScopeType, body.ScopeRef)
	if !ok {
		return
	}

	// Explicit duplicate check, not just the table's UNIQUE: the constraint is
	// UNIQUE(ad_group_id, role, scope_type, scope_ref) and Postgres treats every
	// NULL as distinct from any other NULL, so it does NOT deduplicate
	// 'global'-scoped mappings (scope_ref NULL). Without this, identical rows can
	// be created in pairs. It is exactly the same trap UsersHandler.GrantRole
	// already documents for role_assignments.
	var alreadyExists bool
	if err := h.DB.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM group_role_mappings
		 WHERE ad_group_id = $1 AND role = $2 AND scope_type = $3 AND scope_ref IS NOT DISTINCT FROM $4)`,
		adGroupID, string(role), body.ScopeType, scopeRef,
	).Scan(&alreadyExists); err != nil {
		h.Logger.Error("groupmappings.create: duplicate check failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if alreadyExists {
		http.Error(w, "that mapping already exists for the same group, role and scope", http.StatusConflict)
		return
	}

	var id int64
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO group_role_mappings (ad_group_id, role, scope_type, scope_ref)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		adGroupID, string(role), body.ScopeType, scopeRef,
	).Scan(&id)
	if err != nil {
		// The race between the check above and this INSERT ends here: the
		// UNIQUE of 0001 covers a non-null scope_ref and the partial index of
		// 0030 the NULL one ('global').
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			http.Error(w, "that mapping already exists for the same group, role and scope", http.StatusConflict)
			return
		}
		h.Logger.Error("groupmappings.create: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.audit(r, "groupmapping.create", audit.ResultSuccess, map[string]any{
		"mappingId": id, "adGroupId": adGroupID, "role": string(role),
		"scopeType": body.ScopeType, "scopeRef": body.ScopeRef,
	})
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (h *GroupMappingHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var adGroupID, role, scopeType string
	err = h.DB.QueryRow(r.Context(),
		`DELETE FROM group_role_mappings WHERE id = $1 RETURNING ad_group_id, role, scope_type`, id,
	).Scan(&adGroupID, &role, &scopeType)
	if err != nil {
		// "does not exist" is not distinguished from a real failure in more
		// detail: deleting something that is already gone is not an error that
		// should alarm anyone.
		h.Logger.Warn("groupmappings.delete: could not delete", "error", err, "id", id)
		http.Error(w, "mapping not found", http.StatusNotFound)
		return
	}

	h.audit(r, "groupmapping.delete", audit.ResultSuccess, map[string]any{
		"mappingId": id, "adGroupId": adGroupID, "role": role, "scopeType": scopeType,
	})
	w.WriteHeader(http.StatusNoContent)
}

// resolveScope validates scopeType/scopeRef with the same criteria as
// UsersHandler.GrantRole. In particular, 'namespace' is rejected just the same:
// it exists in the enum but no read filters by namespace, so accepting it here
// would yield a mapping that appears to restrict without restricting anything.
func (h *GroupMappingHandler) resolveScope(w http.ResponseWriter, r *http.Request, scopeType, scopeRef string) (*string, bool) {
	switch scopeType {
	case "global":
		if scopeRef != "" {
			http.Error(w, "scopeRef must be empty for scope 'global'", http.StatusBadRequest)
			return nil, false
		}
		return nil, true
	case "cluster":
		clusterID, err := strconv.ParseInt(scopeRef, 10, 64)
		if err != nil {
			http.Error(w, "scopeRef must be a numeric cluster id", http.StatusBadRequest)
			return nil, false
		}
		var exists bool
		if err := h.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM clusters WHERE id = $1)`, clusterID).Scan(&exists); err != nil {
			h.Logger.Error("groupmappings: failed to check cluster", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return nil, false
		}
		if !exists {
			http.Error(w, "the given cluster does not exist", http.StatusUnprocessableEntity)
			return nil, false
		}
		return &scopeRef, true
	case "namespace":
		http.Error(w, "the 'namespace' scope is not supported yet (it is not applied in any read)", http.StatusUnprocessableEntity)
		return nil, false
	default:
		http.Error(w, "scopeType must be 'global' or 'cluster'", http.StatusBadRequest)
		return nil, false
	}
}

func (h *GroupMappingHandler) audit(r *http.Request, action string, result audit.Result, detail map[string]any) {
	sess, _ := middleware.SessionFromContext(r.Context())
	entry := audit.Entry{
		Action: action, Result: result, IP: middleware.ClientIP(r), Detail: detail,
	}
	if sess != nil {
		entry.UserID = &sess.UserID
		entry.ActorUsername = sess.Username
	}
	audit.Log(r.Context(), h.DB, h.Logger, entry)
}
