package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
)

// Write actions over a cluster. All of them:
//   - resolve the user's effective role OVER THAT cluster to pick the
//     impersonated group (the cluster's native audit sees with which privilege
//     the action was taken), even though the middleware already checked the
//     minimum;
//   - audit success and failure with the affected resource;
//   - return the API server's error as is to the client when it is a request
//     error (403 from the cluster RBAC, 409, 422...), which is what the
//     operator needs to read.

// maxScaleReplicas bounds the value accepted in /scale to prevent a fat-finger
// mistake (e.g. one 0 too many) from causing an accidental massive scale-up.
// Scaling to 0 (pausing) is still allowed: it is a legitimate operation, only
// the upper end is limited.
const maxScaleReplicas = 100

// maxSmallBodyBytes bounds the bodies of the actions that only carry a flag or
// a number (scale, cordon).
const maxSmallBodyBytes = 1 << 12

// maxApplyBytes bounds the pasted YAML. 1 MiB is the cap of an object in etcd.
const maxApplyBytes = 1 << 20

type actor struct {
	userID   int64
	username string
	group    string
}

// resolveActor returns the session and the impersonated group according to the
// effective role. If there is no session it responds 401 and returns ok=false.
func (h *ResourceHandler) resolveActor(w http.ResponseWriter, r *http.Request, _ int64) (actor, bool) {
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return actor{}, false
	}
	// The role on THIS cluster, already capped for read-only tokens: the one
	// RequireClusterRole left in the context. Every caller is mounted behind
	// it (requireCluster*), so re-querying the DB would only risk disagreeing
	// with the middleware.
	role, ok := middleware.RoleFromContext(r.Context())
	if !ok {
		http.Error(w, "no permissions over this cluster", http.StatusForbidden)
		return actor{}, false
	}
	return actor{userID: sess.UserID, username: sess.Username, group: k8s.GroupForRole(role)}, true
}

func (h *ResourceHandler) auditAction(r *http.Request, a actor, action string, clusterID int64, namespace, resource string, err error, detail map[string]any) {
	result := audit.ResultSuccess
	if err != nil {
		result = audit.ResultFailure
		if detail == nil {
			detail = map[string]any{}
		}
		detail["error"] = err.Error()
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &a.userID, ActorUsername: a.username, IP: middleware.ClientIP(r), Action: action,
		Cluster: h.K8s.DisplayName(clusterID), Namespace: namespace, Resource: resource, Result: result, Detail: detail,
	})
}

// writeErr translates the errors of an action. An API server error is returned
// with its text: it is the real explanation (cluster RBAC, admission webhook,
// validation...).
func (h *ResourceHandler) writeErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, k8s.ErrProtectedNamespace), errors.Is(err, k8s.ErrPanelIdentity):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, k8s.ErrInvalidManifest):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, k8s.ErrUnsupportedKind):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, k8s.ErrResourceNotFound):
		http.Error(w, "resource not found", http.StatusNotFound)
	case errors.Is(err, k8s.ErrClusterNotFound):
		http.Error(w, "cluster not found or not connected", http.StatusNotFound)
	default:
		// Error from the managed cluster's API server: 502 to tell it apart from
		// an internal failure of the panel, with the message verbatim.
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
	return true
}

// Apply: POST /api/clusters/{id}/apply {yaml, namespace?, dryRun?}.
func (h *ResourceHandler) Apply(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var body struct {
		YAML      string `json:"yaml"`
		Namespace string `json:"namespace"`
		DryRun    bool   `json:"dryRun"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxApplyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid or too large request body", http.StatusBadRequest)
		return
	}
	objs, err := k8s.ParseManifests(body.YAML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	results, err := h.K8s.Apply(r.Context(), id, objs, strings.TrimSpace(body.Namespace), a.username, a.group, body.DryRun)
	// A dry-run changes nothing: its success is not audited (it would be noise
	// for every preview), but the real apply is, with the list of objects.
	if !body.DryRun || err != nil {
		names := make([]string, 0, len(results))
		for _, res := range results {
			names = append(names, res.Kind+"/"+res.Name)
		}
		h.auditAction(r, a, "resource.apply", id, body.Namespace, strings.Join(names, ","), err, map[string]any{"objects": results, "dryRun": body.DryRun})
	}
	if err != nil {
		// Part of it may have been applied: what was achieved is returned along
		// with the error.
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, k8s.ErrInvalidManifest):
			status = http.StatusBadRequest
		case errors.Is(err, k8s.ErrProtectedNamespace), errors.Is(err, k8s.ErrPanelIdentity):
			status = http.StatusForbidden
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "applied": results})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": results, "dryRun": body.DryRun})
}

// Delete: DELETE /api/clusters/{id}/resources/{kind}/{namespace}/{name}.
func (h *ResourceHandler) DeleteResource(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	kind, namespace, name := r.PathValue("kind"), r.PathValue("namespace"), r.PathValue("name")
	if namespace == "-" {
		namespace = ""
	}
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	err = h.K8s.DeleteResource(r.Context(), id, kind, namespace, name, a.username, a.group)
	h.auditAction(r, a, "resource.delete", id, namespace, name, err, map[string]any{"kind": kind})
	if h.writeErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Restart: POST /api/clusters/{id}/{kind}/{namespace}/{name}/restart.
func (h *ResourceHandler) RestartWorkload(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	kind, namespace, name := r.PathValue("kind"), r.PathValue("namespace"), r.PathValue("name")
	if !k8s.SupportsRestart(kind) {
		http.Error(w, "this kind does not support restart", http.StatusNotFound)
		return
	}
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	err = h.K8s.RestartWorkload(r.Context(), id, kind, namespace, name, a.username, a.group)
	h.auditAction(r, a, singular(kind)+".restart", id, namespace, name, err, nil)
	if h.writeErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Scale: POST /api/clusters/{id}/{kind}/{namespace}/{name}/scale {replicas}.
func (h *ResourceHandler) ScaleWorkload(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	kind, namespace, name := r.PathValue("kind"), r.PathValue("namespace"), r.PathValue("name")
	if !k8s.SupportsScale(kind) {
		http.Error(w, "this kind does not support scale", http.StatusNotFound)
		return
	}
	var body struct {
		Replicas int32 `json:"replicas"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Replicas < 0 || body.Replicas > maxScaleReplicas {
		http.Error(w, "replicas must be between 0 and 100", http.StatusBadRequest)
		return
	}
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	err = h.K8s.ScaleWorkload(r.Context(), id, kind, namespace, name, body.Replicas, a.username, a.group)
	h.auditAction(r, a, singular(kind)+".scale", id, namespace, name, err, map[string]any{"replicas": body.Replicas})
	if h.writeErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Rollback: POST /api/clusters/{id}/deployments/{namespace}/{name}/rollback.
func (h *ResourceHandler) RollbackDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	rev, err := h.K8s.RollbackDeployment(r.Context(), id, namespace, name, a.username, a.group)
	h.auditAction(r, a, "deployment.rollback", id, namespace, name, err, map[string]any{"toRevision": rev})
	if h.writeErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": rev})
}

// Cordon: POST /api/clusters/{id}/nodes/{name}/cordon {cordon: bool}.
func (h *ResourceHandler) CordonNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	var body struct {
		Cordon bool `json:"cordon"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	err = h.K8s.CordonNode(r.Context(), id, name, body.Cordon, a.username, a.group)
	action := "node.cordon"
	if !body.Cordon {
		action = "node.uncordon"
	}
	h.auditAction(r, a, action, id, "", name, err, nil)
	if h.writeErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Drain: POST /api/clusters/{id}/nodes/{name}/drain.
func (h *ResourceHandler) DrainNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	res, err := h.K8s.DrainNode(r.Context(), id, name, a.username, a.group)
	h.auditAction(r, a, "node.drain", id, "", name, err, map[string]any{"evicted": len(res.Evicted), "skipped": len(res.Skipped), "failed": res.Failed})
	if h.writeErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// singular turns the plural of the route into the audit action prefix
// ("deployments" -> "deployment"), which is the existing audit_log convention.
func singular(kind string) string {
	return strings.TrimSuffix(kind, "s")
}
