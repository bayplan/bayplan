// Package agent implements the server side of the agent connector: agents
// deployed in the managed clusters open an OUTBOUND WebSocket to
// /api/agent/connect and, on top of it, a multiplexed tunnel
// (remotedialer) through which the server opens TCP connections
// "inside" the cluster. The k8s Manager uses that tunnel as the rest.Config
// Dial, so informers, impersonation, exec and Helm work unchanged.
//
// The agent does not expose the API server: on its side it only allows
// connections to its own local proxy (127.0.0.1:8081), which adds its
// ServiceAccount token and forwards to the API server. The server never sees
// that token, and the agent handles projected token rotation on its own.
package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rancher/remotedialer"
)

// ProxyAddr is the address the server "dials" inside the tunnel: the agent's
// local proxy towards the API server. It is an agreed name, not a real
// network.
const ProxyAddr = "127.0.0.1:8081"

// Agent handshake headers.
const (
	HeaderClusterID = "X-Bayplan-Cluster"
	HeaderVersion   = "X-Bayplan-Agent-Version"
	// HeaderAgentImage is sent by the server in /api/agent/rbac: the image it
	// expects the agent to be running.
	HeaderAgentImage = "X-Bayplan-Agent-Image"
)

// ManifestTokenTTL is how long the manifest URL is valid after generating it.
const ManifestTokenTTL = time.Hour

var ErrClusterNotFound = errors.New("agent: cluster not found")

// Hub manages agent sessions and notifies connections/disconnections.
type Hub struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	server *remotedialer.Server

	mu        sync.RWMutex
	connected map[int64]*tunnelSession // clusterID -> current session
	nextGen   uint64

	// OnConnect/OnDisconnect are set by main.go so that the k8s Manager
	// (re)connects the cluster when its agent shows up and marks it as down
	// when it leaves.
	OnConnect    func(clusterID int64)
	OnDisconnect func(clusterID int64)
}

func NewHub(pool *pgxpool.Pool, logger *slog.Logger) *Hub {
	h := &Hub{pool: pool, logger: logger, connected: map[int64]*tunnelSession{}}
	h.server = remotedialer.New(h.authorizeOnce, func(rw http.ResponseWriter, req *http.Request, code int, err error) {
		logger.Warn("agent: connection rejected", "remote", req.RemoteAddr, "code", code, "error", err)
		rw.WriteHeader(code)
	})
	return h
}

// authorize validates the handshake: cluster id + agent key (hash in DB).
func (h *Hub) authorize(req *http.Request) (string, bool, error) {
	idStr := req.Header.Get(HeaderClusterID)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "", false, errors.New("invalid cluster header")
	}
	key := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		return "", false, errors.New("missing agent credential")
	}
	var want *string
	var enabled bool
	err = h.pool.QueryRow(req.Context(),
		`SELECT agent_key_hash, enabled FROM clusters WHERE id = $1 AND connector = 'agent'`, id,
	).Scan(&want, &enabled)
	if errors.Is(err, pgx.ErrNoRows) || want == nil {
		return "", false, errors.New("unknown agent cluster")
	}
	if err != nil {
		return "", false, err
	}
	if subtle.ConstantTimeCompare([]byte(hashKey(key)), []byte(*want)) != 1 {
		return "", false, errors.New("incorrect agent credential")
	}
	if !enabled {
		return "", false, errors.New("cluster disabled")
	}
	// Canonical form: remotedialer keys sessions by this string and Dialer
	// looks them up by FormatInt, so "007" must become "7".
	return strconv.FormatInt(id, 10), true, nil
}

// Authenticate validates the agent credential of a regular HTTP request (the
// /api/agent/rbac* routes): same headers and same check as the tunnel
// handshake.
func (h *Hub) Authenticate(r *http.Request) (int64, error) {
	idStr, ok, err := h.authorize(r)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("unauthorized")
		}
		return 0, err
	}
	return strconv.ParseInt(idStr, 10, 64)
}

// tunnelSession is one agent WebSocket. gen tells sessions of the same cluster
// apart: a reconnecting agent opens the new tunnel before the server notices
// the old one is gone (remotedialer only gives up on it after
// PingWaitDuration, 60 s), so the old session's teardown must not mark the
// fresh one offline.
type tunnelSession struct {
	gen   uint64
	since time.Time

	mu      sync.Mutex
	conn    net.Conn // hijacked WebSocket connection, nil until the upgrade
	evicted bool
}

// evict closes the session's connection so that remotedialer drops it right
// away. It matters because remotedialer's dialer returns the OLDEST session
// of a client key, and there is no API to evict it: while a half-dead tunnel
// is still registered, every dial would go through it and fail.
func (s *tunnelSession) evict() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evicted = true
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (s *tunnelSession) setConn(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
	if s.evicted {
		// Evicted between the handshake and the upgrade.
		_ = c.Close()
	}
}

// register makes a new session the current one of the cluster and returns the
// previous one (to evict), if any.
func (h *Hub) register(id int64, now time.Time) (cur, prev *tunnelSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextGen++
	cur = &tunnelSession{gen: h.nextGen, since: now}
	prev = h.connected[id]
	h.connected[id] = cur
	return cur, prev
}

// Evict closes the cluster's live tunnel, if any: called when the cluster is
// deleted or disabled, so that the authenticated WebSocket of a row that no
// longer authorizes anything does not stay registered until the agent
// happens to reconnect. The agent's next handshake is refused by authorize.
func (h *Hub) Evict(id int64) {
	h.mu.Lock()
	s := h.connected[id]
	delete(h.connected, id)
	h.mu.Unlock()
	if s != nil {
		s.evict()
	}
}

// release removes the session if it is still the current one of the cluster
// and reports whether it was: only then is the agent really gone.
func (h *Hub) release(id int64, s *tunnelSession) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connected[id] != s {
		return false
	}
	delete(h.connected, id)
	return true
}

// hijackRecorder captures the connection that the WebSocket upgrader hijacks,
// so that the session can be evicted by closing it.
type hijackRecorder struct {
	http.ResponseWriter
	sess *tunnelSession
}

func (w *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, brw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.sess.setConn(c)
	}
	return c, brw, err
}

func (w *hijackRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type authResultKey struct{}

// authorizeOnce is the remotedialer Authorizer: ServeHTTP already validated
// the handshake (one DB query) and leaves the result in the context; the
// fallback only covers a request that did not come through ServeHTTP.
func (h *Hub) authorizeOnce(req *http.Request) (string, bool, error) {
	if key, ok := req.Context().Value(authResultKey{}).(string); ok {
		return key, true, nil
	}
	return h.authorize(req)
}

// ServeHTTP serves the agent's WebSocket. It blocks for the duration of the
// session, so the before/after is what detects connection and disconnection.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clientKey, ok, err := h.authorize(r)
	if !ok {
		h.logger.Warn("agent: handshake rejected", "remote", r.RemoteAddr, "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, _ := strconv.ParseInt(clientKey, 10, 64)

	sess, prev := h.register(id, time.Now())
	if prev != nil {
		h.logger.Info("agent: replacing the previous tunnel", "cluster", id)
		prev.evict()
	}
	h.touch(id)
	h.logger.Info("agent: connected", "cluster", id, "remote", r.RemoteAddr, "version", r.Header.Get(HeaderVersion))
	if h.OnConnect != nil {
		// In a goroutine: the Manager runs discovery through the tunnel, which
		// is ready once ServeHTTP registers the session; a small delay avoids
		// running before remotedialer publishes it.
		go func() {
			time.Sleep(500 * time.Millisecond)
			h.OnConnect(id)
		}()
	}

	ctx := context.WithValue(r.Context(), authResultKey{}, clientKey)
	h.server.ServeHTTP(&hijackRecorder{ResponseWriter: w, sess: sess}, r.WithContext(ctx))

	if !h.release(id, sess) {
		// A newer tunnel of the same agent took over: it is not a disconnection.
		h.logger.Info("agent: previous tunnel closed", "cluster", id)
		return
	}
	h.touch(id)
	h.logger.Info("agent: disconnected", "cluster", id)
	if h.OnDisconnect != nil {
		h.OnDisconnect(id)
	}
}

func (h *Hub) touch(id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := h.pool.Exec(ctx, `UPDATE clusters SET agent_last_seen = now() WHERE id = $1`, id); err != nil {
		h.logger.Error("agent: failed to update agent_last_seen", "error", err)
	}
}

// Connected reports whether a cluster's agent has the tunnel open.
func (h *Hub) Connected(clusterID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.connected[clusterID]
	return ok && h.server.HasSession(strconv.FormatInt(clusterID, 10))
}

// Dialer returns a Dial function that opens connections through the cluster's
// tunnel. The requested address is ignored: it always dials the agent's local
// proxy, which is the only thing the agent allows.
func (h *Hub) Dialer(clusterID int64) func(ctx context.Context, network, addr string) (net.Conn, error) {
	key := strconv.FormatInt(clusterID, 10)
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		if !h.server.HasSession(key) {
			return nil, fmt.Errorf("agent: the agent of cluster %d is not connected", clusterID)
		}
		return h.server.Dialer(key)(ctx, "tcp", ProxyAddr)
	}
}

// NewKey generates a random (URL-safe) credential and its hash.
func NewKey() (key, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	key = base64.RawURLEncoding.EncodeToString(raw)
	return key, hashKey(key), nil
}

func hashKey(k string) string {
	sum := sha256.Sum256([]byte(k))
	return hex.EncodeToString(sum[:])
}

// HashKey exposes the hash so that the clusters handler can store the key.
func HashKey(k string) string { return hashKey(k) }
