package api

import (
	"net/http"
	"strconv"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/middleware"
)

const (
	defaultLogTailLines = 200
	maxLogTailLines     = 2000
	// Enough for 2000 long lines; beyond that the reader is a person or an LLM
	// and neither gets anything from more.
	maxLogBytes = 512 * 1024
)

// PodLogs: GET /api/clusters/{id}/pods/{namespace}/{name}/logs?container=&tailLines=&previous=
// returns the last lines without following, for the MCP tools and scripts
// (the UI follows over the WebSocket). Same rules as the stream: readonly or
// higher, impersonated with the role on this cluster, the panel's namespace
// refused, and the read audited because logs may carry PII.
func (h *ResourceHandler) PodLogs(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	q := r.URL.Query()
	tail := int64(defaultLogTailLines)
	if v := q.Get("tailLines"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > maxLogTailLines {
			http.Error(w, "tailLines must be between 1 and 2000", http.StatusBadRequest)
			return
		}
		tail = n
	}
	previous := q.Get("previous") == "true"
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	logs, truncated, err := h.K8s.PodLogs(r.Context(), id, namespace, name, q.Get("container"), tail, previous, maxLogBytes, a.username, a.group)
	if err == nil {
		audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
			UserID: &a.userID, ActorUsername: a.username, IP: middleware.ClientIP(r), Action: "pod.logs.read",
			Cluster: h.K8s.DisplayName(id), Namespace: namespace, Resource: name, Result: audit.ResultSuccess,
			Detail: map[string]any{"container": q.Get("container"), "tailLines": tail, "previous": previous},
		})
	}
	if h.writeErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "truncated": truncated, "tailLines": tail})
}
