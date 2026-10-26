package dockerenv

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bayplan/bayplan/internal/k8s"
)

// healthInterval is how often we check that the daemon responds through the tunnel.
const healthInterval = 30 * time.Second

var ErrHostOffline = errors.New("docker: the host's agent is not connected")

type hostConn struct {
	id     int64
	name   string
	client *Client
	stopCh chan struct{}

	mu            sync.RWMutex
	reachable     bool
	version       string
	lastError     string
	lastCheckedAt time.Time
}

// Manager keeps one client per connected Docker host. It is the (much
// simpler) equivalent of k8s.Manager: there is no cache, every read goes to
// the daemon through the tunnel: a Docker host has no watch API to build one on.
type Manager struct {
	mu     sync.RWMutex
	hosts  map[int64]*hostConn
	dialer func(clusterID int64) (DialFunc, bool)
}

func NewManager(dialer func(clusterID int64) (DialFunc, bool)) *Manager {
	return &Manager{hosts: map[int64]*hostConn{}, dialer: dialer}
}

// Connect registers the host when its agent connects. If it was already
// there, it replaces the connection (new tunnel).
func (m *Manager) Connect(id int64, name string) error {
	dial, ok := m.dialer(id)
	if !ok {
		return ErrHostOffline
	}
	h := &hostConn{id: id, name: name, client: NewClient(dial), stopCh: make(chan struct{})}
	m.mu.Lock()
	if old, exists := m.hosts[id]; exists {
		close(old.stopCh)
	}
	m.hosts[id] = h
	m.mu.Unlock()
	go m.healthLoop(h)
	return nil
}

func (m *Manager) Disconnect(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.hosts[id]; ok {
		close(h.stopCh)
		delete(m.hosts, id)
	}
}

// MarkOffline records immediately that the tunnel was closed.
func (m *Manager) MarkOffline(id int64) {
	m.mu.RLock()
	h, ok := m.hosts[id]
	m.mu.RUnlock()
	if !ok {
		return
	}
	h.mu.Lock()
	h.reachable = false
	h.lastError = "agent disconnected: the tunnel to the host has been closed"
	h.mu.Unlock()
}

func (m *Manager) healthLoop(h *hostConn) {
	check := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		info, err := h.client.Info(ctx)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.lastCheckedAt = time.Now()
		if err != nil {
			h.reachable = false
			h.lastError = err.Error()
			return
		}
		h.reachable = true
		h.lastError = ""
		h.version = info.ServerVersion
	}
	check()
	t := time.NewTicker(healthInterval)
	defer t.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-t.C:
			check()
		}
	}
}

// Client returns the host's client, or ErrHostOffline.
func (m *Manager) Client(id int64) (*Client, error) {
	m.mu.RLock()
	h, ok := m.hosts[id]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrHostOffline
	}
	return h.client, nil
}

// Status in the same shape as Kubernetes clusters, so the UI does not have to
// tell them apart: Synced=Connected because there is no cache to sync.
func (m *Manager) Status(id int64) k8s.ClusterStatus {
	m.mu.RLock()
	h, ok := m.hosts[id]
	m.mu.RUnlock()
	if !ok {
		return k8s.ClusterStatus{}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	st := k8s.ClusterStatus{Registered: true, Connected: h.reachable, Synced: h.reachable, ServerVersion: h.version, LastError: h.lastError}
	if !h.lastCheckedAt.IsZero() {
		t := h.lastCheckedAt
		st.LastCheckedAt = &t
	}
	return st
}
