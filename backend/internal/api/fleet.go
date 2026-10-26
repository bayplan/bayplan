package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
)

// FleetHandler serves the fleet view: the status of ALL the clusters the user
// can see, in a single request.
type FleetHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager
}

// Attention returns, per cluster, its connection status, its counters and its
// prioritised problems. It is the "fleet" piece of the panel: until now, to know
// whether something was broken you had to open the clusters one by one.
//
// Permission filtering is done HERE and not with a scope middleware, just like
// in ClusterHandler.List: the request is not against a specific cluster, so
// there is no id to check — the set has to be narrowed. A user with a role on a
// single cluster will see exactly that cluster.
func (h *FleetHandler) Attention(w http.ResponseWriter, r *http.Request) {
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	role, _ := middleware.RoleFromContext(r.Context())

	// Same query as ClusterHandler.List: a global role sees all, and without a
	// global role only those held through a 'cluster'-scoped assignment. Only
	// enabled clusters: a disabled one has no informer and would always show as
	// "not connected", which would be permanent noise.
	query := `SELECT id, display_name FROM clusters WHERE enabled ORDER BY display_name`
	args := []any{}
	if role == "" {
		query = `SELECT c.id, c.display_name
		         FROM clusters c
		         JOIN role_assignments ra ON ra.scope_type = 'cluster' AND ra.scope_ref = c.id::text
		         WHERE ra.user_id = $1 AND c.enabled
		         ORDER BY c.display_name`
		args = []any{sess.UserID}
	}

	rows, err := h.DB.Query(r.Context(), query, args...)
	if err != nil {
		h.Logger.Error("fleet.attention: failed to query clusters", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	clusters := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			h.Logger.Error("fleet.attention: failed to read row", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		clusters[id] = name
	}
	if err := rows.Err(); err != nil {
		h.Logger.Error("fleet.attention: failed to iterate clusters", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, h.K8s.FleetAttention(clusters, time.Now()))
}
