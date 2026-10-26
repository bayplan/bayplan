// Package ws serves real-time pod log streaming over WebSocket.
// Authentication is resolved in the handshake (session cookie validated by
// middleware.RequireSession before accepting the upgrade) and the origin is
// explicitly validated as an additional defense: SameSite=Lax already blocks
// sending the cookie on a cross-site WebSocket started from another page, but
// it is checked here too (anti-CSRF also in the WS handshake).
package ws

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
)

// maxConcurrentStreams limits how many log streams may be open at once in
// the whole backend, so as not to exhaust connections towards the API servers
// of the managed clusters (backpressure).
const maxConcurrentStreams = 50

// logTailLines is how many historical lines are shown when opening the
// stream, before starting to follow live.
const logTailLines = 500

type LogsHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager

	active atomic.Int64
}

// ActiveStreams returns how many log streams are open right now (exposed as
// a gauge in /metrics).
func (h *LogsHandler) ActiveStreams() int64 {
	return h.active.Load()
}

func (h *LogsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if h.active.Load() >= maxConcurrentStreams {
		http.Error(w, "too many active log streams, try again later", http.StatusServiceUnavailable)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid cluster id", http.StatusBadRequest)
		return
	}
	namespace := r.PathValue("namespace")
	podName := r.PathValue("pod")
	container := r.URL.Query().Get("container")

	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	// The impersonated group is decided with the role ON THIS CLUSTER, the
	// one RequireClusterRole left in the context (already capped for
	// read-only tokens), not the global one RequireSession injects: a
	// 'cluster'-scoped admin without a global role would be impersonated as
	// readonly, and recomputing it here would skip the token cap.
	role, ok := middleware.RoleFromContext(r.Context())
	if !ok {
		http.Error(w, "no permissions on this cluster", http.StatusForbidden)
		return
	}
	actingGroup := k8s.GroupForRole(role)
	// The panel's own pods log the first-startup setup token: their logs are
	// as protected as their Secrets.
	if err := h.K8s.GuardNamespace(id, namespace); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.Logger.Error("ws logs: failed to accept connection", "error", err)
		return
	}
	defer conn.CloseNow()

	h.active.Add(1)
	defer h.active.Add(-1)

	// Logs may contain PII: who read them, from which pod and in which
	// cluster is audited, like any other action on a cluster.
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "pod.logs.stream",
		Cluster: h.K8s.DisplayName(id), Namespace: namespace, Resource: podName, Result: audit.ResultSuccess,
	})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stream, err := h.K8s.StreamPodLogs(ctx, id, namespace, podName, container, true, logTailLines, sess.Username, actingGroup)
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, []byte("error: "+err.Error()))
		conn.Close(websocket.StatusInternalError, "failed to open logs")
		return
	}
	defer stream.Close()

	// If the browser closes the connection (tab closed, navigation away), we
	// cancel the context to release the cluster-side log stream instead of
	// leaving it open indefinitely.
	go func() {
		if _, _, err := conn.Read(ctx); err != nil {
			cancel()
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if writeErr := conn.Write(ctx, websocket.MessageText, buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				h.Logger.Warn("ws logs: log stream closed with error", "error", readErr)
			}
			conn.Close(websocket.StatusNormalClosure, "stream finished")
			return
		}
	}
}

func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients (curl, backend tests) do not send Origin
	}
	if origin == "https://"+r.Host {
		return true
	}
	// A plain-http origin is only the panel itself when the panel is served
	// over plain http: on an HTTPS deployment it would be another site that
	// happens to answer on http://<host> (DNS/LAN position).
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return false
	}
	return origin == "http://"+r.Host
}
