package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/kubectl"
	"github.com/bayplan/bayplan/internal/middleware"
)

// KubectlHandler: kubectl web shell (GET /ws/clusters/{id}/kubectl). Same
// role as the pod terminal (admin on the cluster) and same protection (Origin
// + SameSite cookie, no CSRF: see router.go).
//
// Protocol: the browser owns the line editor. JSON text messages from the
// client: {"type":"run","line":"kubectl get pods"} runs;
// {"cols":..,"rows":..} resizes. Binary ones are keyboard input for the
// running process. From the server: binary = process output; JSON text
// {"type":"done","code":n} when each command finishes,
// {"type":"error","message"} for rejections, {"type":"ready"} on open.
type KubectlHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager
	Runner *kubectl.Runner
}

type clientMsg struct {
	Type string `json:"type"`
	Line string `json:"line"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func sendJSON(ctx context.Context, conn *websocket.Conn, v any) {
	b, _ := json.Marshal(v)
	_ = conn.Write(ctx, websocket.MessageText, b)
}

func (h *KubectlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Runner == nil || !h.Runner.Available() {
		http.Error(w, "kubectl is not available on the server", http.StatusNotFound)
		return
	}
	if !originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid cluster id", http.StatusBadRequest)
		return
	}
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	// The namespace becomes the kubeconfig context's default, so it is user
	// input that decides where every command without -n lands: it must be a
	// real namespace name and never the protected one (that would let a
	// plain `kubectl get secrets` read the panel's own Secrets).
	protected := h.K8s.ProtectedNamespace(id)
	namespace := r.URL.Query().Get("namespace")
	if len(validation.IsDNS1123Label(namespace)) > 0 || (protected != "" && namespace == protected) {
		namespace = "default"
	}
	// Always with the admins group: the route already requires admin on the cluster.
	cfg, err := h.K8s.ImpersonatedRestConfig(id, sess.Username, k8s.GroupAdmins)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	shell, err := h.Runner.Open(ctx, cfg, namespace, protected)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer shell.Close()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.Logger.Error("ws kubectl: failed to accept connection", "error", err)
		return
	}
	defer conn.CloseNow()

	clusterName := h.K8s.DisplayName(id)
	start := time.Now()
	commands := 0
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "kubectl.shell",
		Cluster: clusterName, Namespace: namespace, Result: audit.ResultSuccess,
	})
	defer func() {
		audit.Log(context.Background(), h.DB, h.Logger, audit.Entry{
			UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "kubectl.shell.end",
			Cluster: clusterName, Namespace: namespace, Result: audit.ResultSuccess,
			Detail: map[string]any{"commands": commands, "durationMs": time.Since(start).Milliseconds()},
		})
	}()

	sendJSON(ctx, conn, map[string]any{"type": "ready", "namespace": namespace, "cluster": clusterName})

	var (
		mu      sync.Mutex
		current *kubectl.Command
		cols    uint16 = 120
		rows    uint16 = 30
	)
	idle := time.NewTimer(kubectl.IdleTimeout())
	defer idle.Stop()
	go func() {
		select {
		case <-idle.C:
			sendJSON(ctx, conn, map[string]any{"type": "error", "message": "shell closed due to inactivity"})
			cancel()
		case <-ctx.Done():
		}
	}()

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			mu.Lock()
			if current != nil {
				current.Kill()
			}
			mu.Unlock()
			return
		}
		idle.Reset(kubectl.IdleTimeout())
		if msgType == websocket.MessageBinary {
			mu.Lock()
			c := current
			mu.Unlock()
			if c != nil {
				_, _ = c.PTY().Write(data)
			}
			continue
		}
		var msg clientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Cols > 0 && msg.Rows > 0 {
			cols, rows = msg.Cols, msg.Rows
			mu.Lock()
			if current != nil {
				current.Resize(cols, rows)
			}
			mu.Unlock()
			if msg.Type == "" {
				continue
			}
		}
		if msg.Type != "run" {
			continue
		}
		mu.Lock()
		busy := current != nil
		mu.Unlock()
		if busy {
			sendJSON(ctx, conn, map[string]any{"type": "error", "message": "a command is already running"})
			continue
		}
		args, err := kubectl.ValidateProtected(msg.Line, protected, namespace)
		if err != nil {
			if kubectl.IsNoCommand(err) {
				sendJSON(ctx, conn, map[string]any{"type": "done", "code": 0})
				continue
			}
			sendJSON(ctx, conn, map[string]any{"type": "error", "message": err.Error()})
			sendJSON(ctx, conn, map[string]any{"type": "done", "code": 1})
			continue
		}
		cmd, err := shell.Run(args, cols, rows)
		if err != nil {
			sendJSON(ctx, conn, map[string]any{"type": "error", "message": err.Error()})
			sendJSON(ctx, conn, map[string]any{"type": "done", "code": 1})
			continue
		}
		commands++
		// Every command is audited with its arguments: it is what was run
		// against the cluster with the user's identity.
		audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
			UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "kubectl.command",
			Cluster: clusterName, Namespace: namespace, Result: audit.ResultSuccess,
			Detail: map[string]any{"args": args},
		})
		mu.Lock()
		current = cmd
		mu.Unlock()
		go func(c *kubectl.Command) {
			buf := make([]byte, 32*1024)
			for {
				n, rerr := c.PTY().Read(buf)
				if n > 0 {
					if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
						c.Kill()
						break
					}
				}
				if rerr != nil {
					break // EIO when the PTY closes = the process finished
				}
			}
			code := c.Wait()
			mu.Lock()
			if current == c {
				current = nil
			}
			mu.Unlock()
			sendJSON(ctx, conn, map[string]any{"type": "done", "code": code})
		}(cmd)
	}
}

var _ io.Writer = (*wsWriter)(nil)
