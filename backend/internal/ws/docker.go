package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/dockerenv"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
)

// DockerHandler serves Docker container logs and terminal over WebSocket, a
// mirror of LogsHandler/ExecHandler for Kubernetes: same defenses
// (originAllowed + SameSite=Lax, no CSRF because the handshake is GET), same
// auditing and same concurrent session caps (shared with the Kubernetes ones
// through those handlers' counters).
type DockerHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager // only for DisplayName in the audit log
	Docker *dockerenv.Manager
	// Counters shared with the Kubernetes handlers: the streams/terminals cap
	// is global to the backend, not per environment kind.
	LogsCounter *LogsHandler
	ExecCounter *ExecHandler
}

const dockerLogTail = 500

// Logs: GET /ws/clusters/{id}/docker/containers/{cid}/logs
func (h *DockerHandler) Logs(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if h.LogsCounter.active.Load() >= maxConcurrentStreams {
		http.Error(w, "too many active log streams, try again later", http.StatusServiceUnavailable)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	cid := r.PathValue("cid")
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	client, err := h.Docker.Client(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// We need to know whether the container has a TTY to demultiplex or not.
	_, tty, err := client.ContainerInspect(r.Context(), cid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.Logger.Error("ws docker logs: failed to accept connection", "error", err)
		return
	}
	defer conn.CloseNow()
	h.LogsCounter.active.Add(1)
	defer h.LogsCounter.active.Add(-1)

	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "container.logs.stream",
		Cluster: h.K8s.DisplayName(id), Resource: cid, Result: audit.ResultSuccess,
	})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream, err := client.Logs(ctx, cid, dockerLogTail, tty)
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, []byte("error: "+err.Error()))
		conn.Close(websocket.StatusInternalError, "failed to open logs")
		return
	}
	defer stream.Close()
	go func() {
		if _, _, err := conn.Read(ctx); err != nil {
			cancel()
		}
	}()
	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if err := conn.Write(ctx, websocket.MessageText, buf[:n]); err != nil {
				return
			}
		}
		if readErr != nil {
			conn.Close(websocket.StatusNormalClosure, "stream finished")
			return
		}
	}
}

// Terminal: GET /ws/clusters/{id}/docker/containers/{cid}/exec?command=/bin/sh
func (h *DockerHandler) Terminal(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if h.ExecCounter.active.Load() >= maxConcurrentExecs {
		http.Error(w, "too many active terminal sessions, try again later", http.StatusServiceUnavailable)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	cid := r.PathValue("cid")
	shell := r.URL.Query().Get("command")
	if shell == "" {
		shell = "/bin/sh"
	}
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	client, err := h.Docker.Client(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.Logger.Error("ws docker exec: failed to accept connection", "error", err)
		return
	}
	defer conn.CloseNow()
	h.ExecCounter.active.Add(1)
	defer h.ExecCounter.active.Add(-1)

	clusterName := h.K8s.DisplayName(id)
	start := time.Now()
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "container.exec",
		Cluster: clusterName, Resource: cid, Result: audit.ResultSuccess, Detail: map[string]any{"command": shell},
	})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	session, err := client.Exec(ctx, cid, []string{shell})
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, []byte("error: "+err.Error()))
		conn.Close(websocket.StatusInternalError, "failed to open exec")
		return
	}
	defer session.Close()
	// session.Read is not bound to ctx: without this, a browser that goes away
	// leaves the loop below blocked on a silent shell forever, holding the
	// tunnel connection, the process and a slot of ExecCounter.
	go func() {
		<-ctx.Done()
		_ = session.Close()
	}()

	// Keyboard and resize from the browser towards the process.
	go func() {
		defer cancel()
		for {
			msgType, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if msgType == websocket.MessageText {
				var resize struct {
					Cols uint16 `json:"cols"`
					Rows uint16 `json:"rows"`
				}
				if json.Unmarshal(data, &resize) == nil && resize.Cols > 0 {
					_ = session.Resize(ctx, resize.Cols, resize.Rows)
				}
				continue
			}
			if _, err := session.Write(data); err != nil {
				return
			}
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, readErr := session.Read(buf)
		if n > 0 {
			if err := conn.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	cancel()
	exitCtx, exitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	exitCode := session.ExitCode(exitCtx)
	exitCancel()
	audit.Log(context.WithoutCancel(r.Context()), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "container.exec.end",
		Cluster: clusterName, Resource: cid, Result: audit.ResultSuccess,
		Detail: map[string]any{"exitCode": exitCode, "durationMs": time.Since(start).Milliseconds()},
	})
	conn.Close(websocket.StatusNormalClosure, "session finished")
}

var _ io.Reader = (*dockerenv.ExecSession)(nil)
