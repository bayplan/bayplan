package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
)

// maxConcurrentExecs limits how many terminal sessions may be open at once,
// like maxConcurrentStreams for logs — exec is more expensive (bidirectional,
// TTY) so it is capped lower.
const maxConcurrentExecs = 20

// defaultShell is the command launched if the client does not specify one
// (today the frontend always sends an explicit one, so this is only a
// fallback).
const defaultShell = "/bin/bash"

// ExecHandler opens an interactive terminal (pods/exec) over WebSocket. It is
// a mirror of LogsHandler but bidirectional: besides receiving the container
// output, it relays what the user types as the process stdin.
//
// Authorization: the route is mounted with RequireClusterRole(..., RoleAdmin)
// WITHOUT RequireCSRF (see router.go) — RequireCSRF is a no-op on a
// WebSocket's GET handshake (nor can the browser set X-CSRF-Token on `new
// WebSocket(...)`), so the real defense is the same as for logs:
// originAllowed() + SameSite=Lax on the session cookie.
type ExecHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager

	active atomic.Int64
}

// ActiveExecs returns how many terminal sessions are open right now (exposed
// as a gauge in /metrics, like LogsHandler.ActiveStreams).
func (h *ExecHandler) ActiveExecs() int64 {
	return h.active.Load()
}

func (h *ExecHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if h.active.Load() >= maxConcurrentExecs {
		http.Error(w, "too many active terminal sessions, try again later", http.StatusServiceUnavailable)
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
	shell := r.URL.Query().Get("command")
	if shell == "" {
		shell = defaultShell
	}

	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	// No need to look at the role here: the route already requires admin on
	// this cluster (RequireClusterRole in router.go) before reaching this
	// handler. Exec always impersonates with the admins group (see
	// k8s.ExecInPod) — unlike logs, there is no readonly variant.
	// ExecInPod guards the protected namespace too; checking it before the
	// upgrade turns it into a plain 403 instead of an error inside the socket.
	if err := h.K8s.GuardNamespace(id, namespace); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.Logger.Error("ws exec: failed to accept connection", "error", err)
		return
	}
	defer conn.CloseNow()

	h.active.Add(1)
	defer h.active.Add(-1)

	clusterName := h.K8s.DisplayName(id)
	start := time.Now()

	// Start audit: who opens a terminal, in which pod/container, with which
	// command. Exec may expose the container's secrets/PII, so it is audited
	// just like (or with more reason than) log streaming.
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "pod.exec",
		Cluster: clusterName, Namespace: namespace, Resource: podName, Result: audit.ResultSuccess,
		Detail: map[string]any{"command": shell, "container": container},
	})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stdinReader, stdinWriter := io.Pipe()
	sizes := newTerminalSizeQueue()
	out := &wsWriter{ctx: ctx, conn: conn}

	// Reads from the WebSocket: text messages are control (resize), binary
	// ones are keyboard bytes that go straight to the remote process stdin.
	// If the browser closes the connection (tab closed), cancelling the
	// context releases the cluster-side exec instead of leaving it hanging,
	// like in logs.go.
	go func() {
		defer stdinWriter.Close()
		defer sizes.Close()
		for {
			msgType, data, err := conn.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			if msgType == websocket.MessageText {
				var resize struct {
					Cols uint16 `json:"cols"`
					Rows uint16 `json:"rows"`
				}
				if err := json.Unmarshal(data, &resize); err == nil {
					sizes.push(remotecommand.TerminalSize{Width: resize.Cols, Height: resize.Rows})
				}
				continue
			}
			if _, err := stdinWriter.Write(data); err != nil {
				return
			}
		}
	}()

	exitCode, execErr := h.K8s.ExecInPod(ctx, id, k8s.ExecOptions{
		Namespace:     namespace,
		Pod:           podName,
		Container:     container,
		Command:       []string{shell},
		Stdin:         stdinReader,
		Stdout:        out,
		Stderr:        out,
		TTY:           true,
		TerminalSizes: sizes,
	}, sess.Username)

	result := audit.ResultSuccess
	if execErr != nil {
		result = audit.ResultFailure
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "pod.exec.end",
		Cluster: clusterName, Namespace: namespace, Resource: podName, Result: result,
		Detail: map[string]any{"exitCode": exitCode, "durationMs": time.Since(start).Milliseconds()},
	})

	if execErr != nil {
		_ = conn.Write(ctx, websocket.MessageText, []byte("error: "+execErr.Error()))
		conn.Close(websocket.StatusInternalError, "exec session failed")
		return
	}
	conn.Close(websocket.StatusNormalClosure, "session finished")
}

// wsWriter adapts a *websocket.Conn to io.Writer, so it can be passed as
// Stdout/Stderr to the remotecommand executor: every write by the remote
// process is relayed as a binary message.
type wsWriter struct {
	ctx  context.Context
	conn *websocket.Conn
}

func (w *wsWriter) Write(p []byte) (int, error) {
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// terminalSizeQueue implements remotecommand.TerminalSizeQueue from the
// resize messages received over WebSocket. Only the most recent size matters:
// if the executor has not yet consumed the previous one when a new one
// arrives, the old one is dropped instead of accumulating a backlog of stale
// resizes.
type terminalSizeQueue struct {
	sizes chan remotecommand.TerminalSize
}

func newTerminalSizeQueue() *terminalSizeQueue {
	return &terminalSizeQueue{sizes: make(chan remotecommand.TerminalSize, 1)}
}

func (q *terminalSizeQueue) push(s remotecommand.TerminalSize) {
	for {
		select {
		case q.sizes <- s:
			return
		default:
		}
		select {
		case <-q.sizes:
		default:
		}
	}
}

func (q *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	s, ok := <-q.sizes
	if !ok {
		return nil
	}
	return &s
}

func (q *terminalSizeQueue) Close() {
	close(q.sizes)
}
