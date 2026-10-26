package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/registries"
)

// RegistriesHandler: image registries (platform, global admin only) and their
// bindings per cluster/namespace (editor over the cluster: creating a pull
// secret in a namespace is a workload operation, not a platform one). The
// password never leaves: `passwordSet: true`.
type RegistriesHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager
	Svc    *registries.Service
}

type registryResponse struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Server          string    `json:"server"`
	Username        string    `json:"username"`
	Email           string    `json:"email,omitempty"`
	InsecureSkipTLS bool      `json:"insecureSkipTLS"`
	SecretName      string    `json:"secretName"`
	BindingCount    int       `json:"bindingCount"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func toRegistryResponse(r registries.Registry) registryResponse {
	return registryResponse{ID: r.ID, Name: r.Name, Server: r.Server, Username: r.Username, Email: r.Email, InsecureSkipTLS: r.InsecureSkipTLS,
		SecretName: r.SecretName(), BindingCount: r.BindingCount, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type registryInput struct {
	Name            *string `json:"name"`
	Server          *string `json:"server"`
	Username        *string `json:"username"`
	Password        *string `json:"password"`
	Email           *string `json:"email"`
	InsecureSkipTLS *bool   `json:"insecureSkipTLS"`
}

func (in *registryInput) applyTo(r *registries.Registry) (passwordChanged bool) {
	if in.Server != nil {
		r.Server = *in.Server
	}
	if in.Username != nil {
		r.Username = *in.Username
	}
	if in.Email != nil {
		r.Email = *in.Email
	}
	if in.InsecureSkipTLS != nil {
		r.InsecureSkipTLS = *in.InsecureSkipTLS
	}
	if in.Password != nil && *in.Password != "" {
		r.Password = strings.TrimSpace(*in.Password)
		return true
	}
	return false
}

// List: without passwords; every user with a session sees it because the
// binding form (editor) needs to pick a registry.
func (h *RegistriesHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.Svc.Store.List(r.Context())
	if err != nil {
		h.Logger.Error("registries.list: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A cluster editor binds a registry by name; the account behind it is
	// the platform admin's business.
	role, _ := middleware.RoleFromContext(r.Context())
	out := make([]registryResponse, 0, len(list))
	for _, x := range list {
		resp := toRegistryResponse(x)
		if !role.IsAdmin() {
			resp.Username, resp.Email = "", ""
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *RegistriesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in registryInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if in.Name == nil {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	reg := registries.Registry{Name: *in.Name}
	in.applyTo(&reg)
	if err := reg.Validate(true); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := registries.Validate(r.Context(), &reg); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	id, err := h.Svc.Store.Create(r.Context(), &reg)
	if errors.Is(err, registries.ErrDuplicate) {
		http.Error(w, "a registry with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		h.Logger.Error("registries.create: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "registry.create", 0, reg.Name, nil, map[string]any{"server": reg.Server, "username": reg.Username})
	created, err := h.Svc.Store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toRegistryResponse(*created))
}

// Update edits server/user/password (rotation) and resyncs all the bindings in
// the background with the identity of the admin who rotates.
func (h *RegistriesHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in registryInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	reg, err := h.Svc.Store.Get(r.Context(), id)
	if errors.Is(err, registries.ErrNotFound) {
		http.Error(w, "registry not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("registries.update: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	in.applyTo(reg)
	if err := reg.Validate(true); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := registries.Validate(r.Context(), reg); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := h.Svc.Store.Update(r.Context(), reg); err != nil {
		h.Logger.Error("registries.update: save failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess, _ := middleware.SessionFromContext(r.Context())
	// Global admin: their impersonated group is the admins one in every cluster.
	h.Svc.SyncRegistry(id, sess.Username, k8s.GroupAdmins)
	h.audit(r, "registry.update", 0, reg.Name, nil, map[string]any{"server": reg.Server, "bindings": reg.BindingCount})
	reg.UpdatedAt = time.Now()
	writeJSON(w, http.StatusOK, toRegistryResponse(*reg))
}

// Delete removes the registry and its bindings (CASCADE), first trying to
// withdraw the Secrets from the clusters. An unreachable cluster does not block
// the deletion: it is logged and the Secret is left orphaned with the
// managed-by label.
func (h *RegistriesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	reg, err := h.Svc.Store.Get(r.Context(), id)
	if errors.Is(err, registries.ErrNotFound) {
		http.Error(w, "registry not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("registries.delete: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess, _ := middleware.SessionFromContext(r.Context())
	bindings, _ := h.Svc.Store.BindingsForRegistry(r.Context(), id)
	removed, failed := 0, 0
	for _, b := range bindings {
		if err := h.Svc.Remove(r.Context(), b, sess.Username, k8s.GroupAdmins); err != nil {
			failed++
			h.Logger.Warn("registries.delete: could not withdraw the secret", "cluster", b.ClusterID, "namespace", b.Namespace, "error", err)
		} else {
			removed++
		}
	}
	if err := h.Svc.Store.Delete(r.Context(), id); err != nil {
		h.Logger.Error("registries.delete: delete failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "registry.delete", 0, reg.Name, nil, map[string]any{"secretsRemoved": removed, "secretsFailed": failed})
	writeJSON(w, http.StatusOK, map[string]any{"secretsRemoved": removed, "secretsFailed": failed})
}

type bindingResponse struct {
	ID              int64      `json:"id"`
	RegistryID      int64      `json:"registryId"`
	RegistryName    string     `json:"registryName"`
	Server          string     `json:"server"`
	ClusterID       int64      `json:"clusterId"`
	Namespace       string     `json:"namespace"`
	SecretName      string     `json:"secretName"`
	AttachDefaultSA bool       `json:"attachDefaultSA"`
	LastSyncedAt    *time.Time `json:"lastSyncedAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	CreatedBy       string     `json:"createdBy,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

func toBindingResponse(b registries.Binding) bindingResponse {
	return bindingResponse{ID: b.ID, RegistryID: b.RegistryID, RegistryName: b.RegistryName, Server: b.Server, ClusterID: b.ClusterID,
		Namespace: b.Namespace, SecretName: b.SecretName(), AttachDefaultSA: b.AttachDefaultSA, LastSyncedAt: b.LastSyncedAt, LastError: b.LastError,
		CreatedBy: b.CreatedBy, CreatedAt: b.CreatedAt}
}

// actingIdentity: session user and group according to their effective role
// over the cluster (resolved by the middleware).
func actingIdentity(r *http.Request) (string, string) {
	sess, _ := middleware.SessionFromContext(r.Context())
	role, _ := middleware.RoleFromContext(r.Context())
	user := ""
	if sess != nil {
		user = sess.Username
	}
	// No default towards editor: a route mounted without the cluster-role
	// middleware falls to the readonly group, which cannot write.
	return user, k8s.GroupForRole(role)
}

func (h *RegistriesHandler) ListBindings(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	list, err := h.Svc.Store.BindingsForCluster(r.Context(), id)
	if err != nil {
		h.Logger.Error("registries.bindings: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]bindingResponse, 0, len(list))
	for _, b := range list {
		out = append(out, toBindingResponse(b))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateBinding creates the binding and syncs it on the spot: if the cluster
// rejects the Secret (RBAC, non-existent namespace) the error comes back in
// the response and the binding stays saved with last_error, to retry.
func (h *RegistriesHandler) CreateBinding(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in struct {
		RegistryID      int64  `json:"registryId"`
		Namespace       string `json:"namespace"`
		AttachDefaultSA *bool  `json:"attachDefaultSA"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	user, group := actingIdentity(r)
	b := registries.Binding{RegistryID: in.RegistryID, ClusterID: id, Namespace: in.Namespace, AttachDefaultSA: true, CreatedBy: user}
	if in.AttachDefaultSA != nil {
		b.AttachDefaultSA = *in.AttachDefaultSA
	}
	if err := b.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	bid, err := h.Svc.Store.CreateBinding(r.Context(), &b)
	switch {
	case errors.Is(err, registries.ErrDuplicate):
		http.Error(w, "that registry is already bound to that namespace", http.StatusConflict)
		return
	case errors.Is(err, registries.ErrNotFound):
		http.Error(w, "registry not found", http.StatusBadRequest)
		return
	case err != nil:
		h.Logger.Error("registries.bind: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	created, err := h.Svc.Store.Binding(r.Context(), bid)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	syncErr := h.Svc.Sync(r.Context(), *created, user, group)
	h.audit(r, "registry.bind", id, created.RegistryName, syncErr, map[string]any{"namespace": created.Namespace, "attachDefaultSA": created.AttachDefaultSA})
	created, _ = h.Svc.Store.Binding(r.Context(), bid)
	status := http.StatusCreated
	if syncErr != nil {
		// 207: the binding exists but the first sync failed; the UI shows
		// last_error and offers to retry.
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, toBindingResponse(*created))
}

func (h *RegistriesHandler) bindingFor(w http.ResponseWriter, r *http.Request) (*registries.Binding, bool) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return nil, false
	}
	bid, err := strconv.ParseInt(r.PathValue("bid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return nil, false
	}
	b, err := h.Svc.Store.Binding(r.Context(), bid)
	// The binding has to belong to THIS cluster: the middleware authorized the
	// {id} of the route, not the one of the row.
	if errors.Is(err, registries.ErrNotFound) || (err == nil && b.ClusterID != id) {
		http.Error(w, "binding not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		h.Logger.Error("registries.binding: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	return b, true
}

func (h *RegistriesHandler) SyncBinding(w http.ResponseWriter, r *http.Request) {
	b, ok := h.bindingFor(w, r)
	if !ok {
		return
	}
	user, group := actingIdentity(r)
	err := h.Svc.Sync(r.Context(), *b, user, group)
	h.audit(r, "registry.sync", b.ClusterID, b.RegistryName, err, map[string]any{"namespace": b.Namespace})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	updated, _ := h.Svc.Store.Binding(r.Context(), b.ID)
	writeJSON(w, http.StatusOK, toBindingResponse(*updated))
}

// DeleteBinding withdraws the Secret from the cluster and deletes the binding.
// If the cluster does not respond it is deleted anyway (the row no longer
// describes anything real) and the warning is returned.
func (h *RegistriesHandler) DeleteBinding(w http.ResponseWriter, r *http.Request) {
	b, ok := h.bindingFor(w, r)
	if !ok {
		return
	}
	user, group := actingIdentity(r)
	removeErr := h.Svc.Remove(r.Context(), *b, user, group)
	if err := h.Svc.Store.DeleteBinding(r.Context(), b.ID); err != nil {
		h.Logger.Error("registries.unbind: delete failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "registry.unbind", b.ClusterID, b.RegistryName, removeErr, map[string]any{"namespace": b.Namespace})
	if removeErr != nil {
		writeJSON(w, http.StatusMultiStatus, map[string]any{"warning": "binding deleted, but the Secret could not be withdrawn from the cluster: " + removeErr.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RegistriesHandler) audit(r *http.Request, action string, clusterID int64, name string, err error, detail map[string]any) {
	sess, _ := middleware.SessionFromContext(r.Context())
	var uid *int64
	var uname string
	if sess != nil {
		uid, uname = &sess.UserID, sess.Username
	}
	result := audit.ResultSuccess
	if err != nil {
		result = audit.ResultFailure
		if detail == nil {
			detail = map[string]any{}
		}
		detail["error"] = err.Error()
	}
	cluster := ""
	if clusterID != 0 {
		cluster = h.K8s.DisplayName(clusterID)
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: uid, ActorUsername: uname, IP: middleware.ClientIP(r), Action: action,
		Cluster: cluster, Resource: name, Result: result, Detail: detail,
	})
}
