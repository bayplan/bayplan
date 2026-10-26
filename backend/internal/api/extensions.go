package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/extensions"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExtensionsHandler: links to external tools per cluster. Management is global
// admin (/api/extensions); the per-cluster listing
// (/api/clusters/{id}/extensions) returns only those the user's role on that
// cluster may see.
type ExtensionsHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	Store  *extensions.Store
}

func (h *ExtensionsHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.List(r.Context())
	if err != nil {
		h.Logger.Error("extensions.list: failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// ListForCluster uses the role for THIS cluster that RequireClusterRole leaves
// in the context (already capped by a read-only token).
func (h *ExtensionsHandler) ListForCluster(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	role, ok := middleware.RoleFromContext(r.Context())
	if !ok {
		role = rbac.RoleReadonly
	}
	list, err := h.Store.ListForCluster(r.Context(), id, role)
	if err != nil {
		h.Logger.Error("extensions.list: failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *ExtensionsHandler) decode(w http.ResponseWriter, r *http.Request) (extensions.Input, bool) {
	var in extensions.Input
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return in, false
	}
	if err := extensions.Validate(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return in, false
	}
	if in.ClusterID != nil {
		var exists bool
		if err := h.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM clusters WHERE id = $1)", *in.ClusterID).Scan(&exists); err != nil || !exists {
			http.Error(w, "clusterId does not exist", http.StatusBadRequest)
			return in, false
		}
	}
	return in, true
}

func (h *ExtensionsHandler) audit(r *http.Request, action string, e extensions.Extension) {
	sess, _ := middleware.SessionFromContext(r.Context())
	if sess == nil {
		return
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, ActorUsername: sess.Username, IP: middleware.ClientIP(r), Action: action,
		Resource: e.Name, Result: audit.ResultSuccess,
		Detail: map[string]any{"id": e.ID, "url": e.URL, "mode": e.Mode, "minRole": e.MinRole, "clusterId": e.ClusterID},
	})
}

func (h *ExtensionsHandler) Create(w http.ResponseWriter, r *http.Request) {
	in, ok := h.decode(w, r)
	if !ok {
		return
	}
	e, err := h.Store.Create(r.Context(), in)
	if err != nil {
		h.Logger.Error("extensions.create: failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "extension.create", e)
	writeJSON(w, http.StatusCreated, e)
}

func (h *ExtensionsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	in, ok := h.decode(w, r)
	if !ok {
		return
	}
	e, err := h.Store.Update(r.Context(), id, in)
	if errors.Is(err, extensions.ErrNotFound) {
		http.Error(w, "extension not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("extensions.update: failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "extension.update", e)
	writeJSON(w, http.StatusOK, e)
}

func (h *ExtensionsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	e, err := h.Store.Delete(r.Context(), id)
	if errors.Is(err, extensions.ErrNotFound) {
		http.Error(w, "extension not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("extensions.delete: failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "extension.delete", e)
	w.WriteHeader(http.StatusNoContent)
}
