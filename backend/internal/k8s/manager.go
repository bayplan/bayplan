package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	appsv1listers "k8s.io/client-go/listers/apps/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/bayplan/bayplan/internal/crypto"
)

// resyncPeriod is how often the informer fully re-lists the resource as a
// safety net, on top of the incremental watches. It does not affect the panel's
// read latency (which always reads from the cache).
const resyncPeriod = 10 * time.Minute

// healthCheckInterval is how often it is checked live that the API server
// responds and accepts the credential. It exists because an informer that
// synced once stays marked as synced even if the token expires afterwards: the
// reflectors retry with backoff silently and the cache goes stale without
// anyone noticing. A call to /version every 30 s is cheap and detects 401/403
// and network outages with the real error.
const healthCheckInterval = 30 * time.Second

// clusterConn is the in-memory state of the connection to a cluster: base
// client, active informers and their sync state.
type clusterConn struct {
	id          int64
	displayName string
	inCluster   bool // incluster connector: the server's own cluster
	viaAgent    bool // agent connector: the agent's namespace is protected

	restConfig rest.Config // base, without impersonation; actions clone it and add Impersonate
	// clientset is the same base client that starts the informer factory (it
	// impersonates nobody). It is kept here to reuse it in calls that have no
	// informer of their own, avoiding a new clientset per request.
	clientset *kubernetes.Clientset
	factory   informers.SharedInformerFactory
	stopCh    chan struct{}

	// required are the informers without which the panel cannot serve anything
	// (pods, deployments, nodes…); WaitForCacheSync waits for ALL of them and a
	// missing RBAC permission leaves the whole cluster at 503, on purpose: a
	// "half-synced" cluster complicates everything else.
	required []cache.SharedIndexInformer
	// optional are informers of secondary types (ingress, jobs, configmaps…)
	// that are checked individually on read: if the cluster lacks the
	// permission for one, only that listing responds 503 and the rest goes on.
	// Their listers and errors live in opt (see optional.go).
	optional map[string]cache.SharedIndexInformer
	opt      *optionalListers

	podLister         corev1listers.PodLister
	deploymentLister  appsv1listers.DeploymentLister
	statefulSetLister appsv1listers.StatefulSetLister
	daemonSetLister   appsv1listers.DaemonSetLister
	// replicaSetLister feeds no view of its own: it is needed to resolve
	// pod -> deployment, which in Kubernetes always goes through the
	// intermediate ReplicaSet (ownerReferences in two hops).
	replicaSetLister appsv1listers.ReplicaSetLister
	serviceLister    corev1listers.ServiceLister
	nodeLister       corev1listers.NodeLister
	namespaceLister  corev1listers.NamespaceLister
	eventLister      corev1listers.EventLister

	// metrics is the latest metrics-server snapshot (see metrics.go).
	metrics metricsState

	statusMu      sync.RWMutex
	synced        bool
	reachable     bool
	serverVersion string
	lastError     string
	lastCheckedAt time.Time
}

// Manager manages the connection and the informer cache of every enabled
// cluster. It is safe for concurrent use.
type Manager struct {
	mu      sync.RWMutex
	conns   map[int64]*clusterConn
	pool    *pgxpool.Pool
	keyring *crypto.Keyring
	logger  *slog.Logger

	// agentDialer is set by main.go with the agent Hub: it returns the tunnel's
	// Dial function for a cluster with the "agent" connector, or false if the
	// agent is not connected right now.
	agentDialer func(clusterID int64) (DialFunc, bool)
	// cloud is set by main.go with internal/cloud.Service: it builds the
	// rest.Config of a cluster with the "cloud" connector (the provider's
	// endpoint and CA + a short-lived token that renews itself).
	cloud CloudConnector
}

// CloudConnector is what the Manager needs from the cloud package, as an
// interface so k8s does not drag in the provider SDKs.
type CloudConnector interface {
	RestConfig(ctx context.Context, credentialID int64, ref json.RawMessage) (*rest.Config, error)
}

// SetCloudConnector wires the Manager to the cloud credentials.
func (m *Manager) SetCloudConnector(c CloudConnector) {
	m.cloud = c
}

// DialFunc is the signature of rest.Config.Dial.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// SetAgentDialer wires the Manager to the agent Hub.
func (m *Manager) SetAgentDialer(fn func(clusterID int64) (DialFunc, bool)) {
	m.agentDialer = fn
}

func NewManager(pool *pgxpool.Pool, keyring *crypto.Keyring, logger *slog.Logger) *Manager {
	return &Manager{conns: make(map[int64]*clusterConn), pool: pool, keyring: keyring, logger: logger}
}

const clusterColumns = `id, display_name, kind, connector, api_endpoint, ca_cert, insecure_skip_tls_verify,
	sa_token_encrypted, client_cert, client_key_encrypted, key_id, cloud_credential_id, cloud_ref`

func scanCluster(row pgx.Row) (ClusterConfig, error) {
	var c ClusterConfig
	var caCert, clientCert, keyID *string
	if err := row.Scan(&c.ID, &c.DisplayName, &c.Kind, &c.Connector, &c.APIEndpoint, &caCert, &c.Insecure,
		&c.SATokenEncrypted, &clientCert, &c.ClientKeyEncrypted, &keyID, &c.CloudCredentialID, &c.CloudRef); err != nil {
		return ClusterConfig{}, err
	}
	if caCert != nil {
		c.CACert = *caCert
	}
	if clientCert != nil {
		c.ClientCert = *clientCert
	}
	if keyID != nil {
		c.KeyID = *keyID
	}
	return c, nil
}

// LoadCluster reads the persisted configuration of a cluster (credentials
// still encrypted). Handlers use it to reconnect after editing or enabling.
func (m *Manager) LoadCluster(ctx context.Context, id int64) (ClusterConfig, error) {
	c, err := scanCluster(m.pool.QueryRow(ctx, `SELECT `+clusterColumns+` FROM clusters WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ClusterConfig{}, ErrClusterNotFound
	}
	if err != nil {
		return ClusterConfig{}, fmt.Errorf("k8s: failed to read cluster %d: %w", id, err)
	}
	return c, nil
}

// LoadAll connects every enabled cluster when the backend starts. An
// unreachable cluster does not abort the panel's startup: it is recorded as
// not connected (see ClusterStatus) and the UI reflects it.
func (m *Manager) LoadAll(ctx context.Context) error {
	rows, err := m.pool.Query(ctx, `SELECT `+clusterColumns+` FROM clusters WHERE enabled = TRUE`)
	if err != nil {
		return fmt.Errorf("k8s: failed to list clusters: %w", err)
	}
	defer rows.Close()

	var configs []ClusterConfig
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return fmt.Errorf("k8s: failed to read cluster: %w", err)
		}
		configs = append(configs, c)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("k8s: failed to iterate clusters: %w", err)
	}

	for _, c := range configs {
		if err := m.Connect(c); err != nil {
			if errors.Is(err, ErrNotKubernetes) {
				continue
			}
			if errors.Is(err, ErrAgentOffline) {
				// Normal at startup: the agent will reconnect on its own and the
				// Hub will call Connect then.
				m.logger.Info("k8s: agent cluster waiting for its agent", "cluster", c.DisplayName)
				continue
			}
			m.logger.Error("k8s: failed to connect cluster at startup", "cluster", c.DisplayName, "error", err)
		}
	}
	return nil
}

// Decrypt returns the clear-text credentials of a persisted configuration.
func (m *Manager) Decrypt(c ClusterConfig) (Credentials, error) {
	creds := Credentials{APIEndpoint: c.APIEndpoint, CACert: c.CACert, Insecure: c.Insecure, ClientCert: c.ClientCert}
	if len(c.SATokenEncrypted) > 0 {
		token, err := m.keyring.Decrypt(crypto.KeyID(c.KeyID), c.SATokenEncrypted)
		if err != nil {
			return Credentials{}, fmt.Errorf("k8s: failed to decrypt token of cluster %q: %w", c.DisplayName, err)
		}
		creds.Token = string(token)
	}
	if len(c.ClientKeyEncrypted) > 0 {
		key, err := m.keyring.Decrypt(crypto.KeyID(c.KeyID), c.ClientKeyEncrypted)
		if err != nil {
			return Credentials{}, fmt.Errorf("k8s: failed to decrypt client key of cluster %q: %w", c.DisplayName, err)
		}
		creds.ClientKey = string(key)
	}
	return creds, nil
}

// Connect (re)connects a cluster: decrypts its credentials, builds the base
// client and starts its informers in the background. If the cluster was
// already connected, it closes the previous connection first (after editing
// its credentials or enabling it again).
func (m *Manager) Connect(c ClusterConfig) error {
	if c.Kind == "docker" {
		// Docker hosts have no informers: dockerenv.Manager handles them.
		return ErrNotKubernetes
	}
	var restCfg rest.Config
	if c.Connector == "agent" {
		// The server "dials" the agent's local proxy through the tunnel; the
		// proxy adds the agent SA's token and forwards to the API server over
		// TLS. Here the connection is plain HTTP INSIDE the tunnel (which is
		// encrypted in the agent's TLS WebSocket), hence no CA and no token.
		if m.agentDialer == nil {
			return fmt.Errorf("k8s: cluster %q uses an agent but there is no agent hub", c.DisplayName)
		}
		dial, ok := m.agentDialer(c.ID)
		if !ok {
			return fmt.Errorf("%w: the agent of %q is not connected", ErrAgentOffline, c.DisplayName)
		}
		restCfg = rest.Config{Host: "http://agent.bayplan.internal", Dial: dial}
	} else if c.Connector == ConnectorInCluster {
		// The server pod's identity: projected token (client-go re-reads it on
		// rotation) and the cluster's CA. The RBAC is granted by the chart.
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf("k8s: cluster %q is in-cluster but the server is not running in Kubernetes: %w", c.DisplayName, err)
		}
		restCfg = *cfg
	} else if c.Connector == "cloud" {
		if m.cloud == nil || c.CloudCredentialID == nil {
			return fmt.Errorf("k8s: cluster %q uses a cloud credential but there is no cloud connector", c.DisplayName)
		}
		// Describing the cluster on every connection costs one call to the
		// provider and in exchange picks up a rotated CA or endpoint without
		// anyone having to edit anything.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cfg, err := m.cloud.RestConfig(ctx, *c.CloudCredentialID, c.CloudRef)
		if err != nil {
			return fmt.Errorf("k8s: failed to connect %q via cloud credential: %w", c.DisplayName, err)
		}
		restCfg = *cfg
		// The cloud connector sets a per-request Timeout; the base config
		// feeds the informers' watches, which that would cut (see below).
		restCfg.Timeout = 0
	} else {
		creds, err := m.Decrypt(c)
		if err != nil {
			return err
		}
		restCfg = creds.RestConfig()
	}
	// NO rest.Config.Timeout on the base config, whatever the connector: it
	// is http.Client.Timeout, which also bounds reading the body, so every
	// informer watch would be cut and reconnect every 10 s and `logs -f`
	// would die after 10 s. The short calls (health check, metrics, Validate,
	// discovery) are bounded with a context or with boundedConfig.

	clientset, err := kubernetes.NewForConfig(&restCfg)
	if err != nil {
		return fmt.Errorf("k8s: failed to create client for %q: %w", c.DisplayName, err)
	}

	factory := informers.NewSharedInformerFactory(clientset, resyncPeriod)
	podInformer := factory.Core().V1().Pods()
	deploymentInformer := factory.Apps().V1().Deployments()
	statefulSetInformer := factory.Apps().V1().StatefulSets()
	daemonSetInformer := factory.Apps().V1().DaemonSets()
	replicaSetInformer := factory.Apps().V1().ReplicaSets()
	serviceInformer := factory.Core().V1().Services()
	nodeInformer := factory.Core().V1().Nodes()
	namespaceInformer := factory.Core().V1().Namespaces()
	eventInformer := factory.Core().V1().Events()

	conn := &clusterConn{
		id:          c.ID,
		displayName: c.DisplayName,
		inCluster:   c.Connector == ConnectorInCluster,
		viaAgent:    c.Connector == "agent",
		restConfig:  restCfg,
		clientset:   clientset,
		factory:     factory,
		stopCh:      make(chan struct{}),
		required: []cache.SharedIndexInformer{
			podInformer.Informer(), deploymentInformer.Informer(), statefulSetInformer.Informer(),
			daemonSetInformer.Informer(), replicaSetInformer.Informer(), serviceInformer.Informer(),
			nodeInformer.Informer(), namespaceInformer.Informer(), eventInformer.Informer(),
		},
		optional:          map[string]cache.SharedIndexInformer{},
		podLister:         podInformer.Lister(),
		deploymentLister:  deploymentInformer.Lister(),
		statefulSetLister: statefulSetInformer.Lister(),
		daemonSetLister:   daemonSetInformer.Lister(),
		replicaSetLister:  replicaSetInformer.Lister(),
		serviceLister:     serviceInformer.Lister(),
		nodeLister:        nodeInformer.Lister(),
		namespaceLister:   namespaceInformer.Lister(),
		eventLister:       eventInformer.Lister(),
	}
	m.registerOptionalInformers(conn)

	// List/watch errors (401 from an expired token, 403 from incomplete RBAC,
	// network) are captured here: by default client-go only writes them to its
	// own log and the UI never learns WHY there is no data.
	for _, inf := range conn.required {
		_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) { conn.recordWatchError(err) })
	}

	m.mu.Lock()
	if old, exists := m.conns[c.ID]; exists {
		close(old.stopCh)
	}
	m.conns[c.ID] = conn
	m.mu.Unlock()

	factory.Start(conn.stopCh)

	go func() {
		ok := cache.WaitForCacheSync(conn.stopCh, hasSyncedFuncs(conn.required)...)
		conn.statusMu.Lock()
		conn.synced = ok
		if ok {
			conn.lastError = ""
		} else if conn.lastError == "" {
			conn.lastError = "failed to sync informers: check the cluster's endpoint, CA and credentials"
		}
		conn.statusMu.Unlock()

		if ok {
			m.logger.Info("k8s: cluster synced", "cluster", c.DisplayName)
		} else {
			m.logger.Warn("k8s: cluster not synced", "cluster", c.DisplayName)
		}
	}()

	go m.healthLoop(conn)
	go m.metricsLoop(conn)

	return nil
}

func hasSyncedFuncs(infs []cache.SharedIndexInformer) []cache.InformerSynced {
	out := make([]cache.InformerSynced, 0, len(infs))
	for _, inf := range infs {
		out = append(out, inf.HasSynced)
	}
	return out
}

// recordWatchError translates the client-go error into something actionable
// for the admin. A 401/403 is almost always an expired credential or RBAC not
// re-applied.
func (c *clusterConn) recordWatchError(err error) {
	msg := describeAPIError(err)
	c.statusMu.Lock()
	c.lastError = msg
	c.statusMu.Unlock()
}

func describeAPIError(err error) string {
	switch {
	case apierrors.IsUnauthorized(err):
		return "credential rejected by the API server (401): token expired or revoked"
	case apierrors.IsForbidden(err):
		return "permission denied (403): re-apply the ServiceAccount RBAC in the cluster — " + err.Error()
	default:
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return msg
	}
}

// healthLoop checks live, every healthCheckInterval, that the API server
// responds with the stored credential. It sets reachable/serverVersion/lastError;
// it ends when stopCh is closed (disconnect or reconnection).
func (m *Manager) healthLoop(conn *clusterConn) {
	check := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		v, err := conn.clientset.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
		conn.statusMu.Lock()
		defer conn.statusMu.Unlock()
		conn.lastCheckedAt = time.Now()
		if err != nil {
			conn.reachable = false
			conn.lastError = describeAPIError(err)
			return
		}
		conn.reachable = true
		if ver := parseGitVersion(v); ver != "" {
			conn.serverVersion = ver
		}
		// If the live check succeeds and the cache is synced, the last watch
		// error no longer describes the current state.
		if conn.synced {
			conn.lastError = ""
		}
	}
	check()
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-conn.stopCh:
			return
		case <-ticker.C:
			check()
		}
	}
}

// parseGitVersion extracts "gitVersion" from the /version JSON.
func parseGitVersion(raw []byte) string {
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return v.GitVersion
}

// MarkAgentOffline records immediately that the agent's tunnel has closed,
// without waiting for the next health check (up to 30 s): the UI switches to
// "not connected" with the right reason.
func (m *Manager) MarkAgentOffline(id int64) {
	conn, err := m.getConn(id)
	if err != nil {
		return
	}
	conn.statusMu.Lock()
	conn.reachable = false
	conn.lastError = "agent disconnected: the tunnel to the cluster has closed"
	conn.statusMu.Unlock()
}

// Disconnect stops a cluster's informers and releases its connection (used
// when disabling or deleting a cluster).
func (m *Manager) Disconnect(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if conn, ok := m.conns[id]; ok {
		close(conn.stopCh)
		delete(m.conns, id)
	}
}

// ClusterReadiness implements api.ReadinessSource for /readyz (informational:
// a down cluster no longer leaves the whole application "not ready").
func (m *Manager) ClusterReadiness() []ClusterReadiness {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ClusterReadiness, 0, len(m.conns))
	for _, c := range m.conns {
		c.statusMu.RLock()
		out = append(out, ClusterReadiness{Cluster: c.displayName, InformerSynced: c.synced, LastError: c.lastError})
		c.statusMu.RUnlock()
	}
	return out
}

// ClusterStatus returns a cluster's connection state for the API. An
// unregistered (disabled) cluster returns Registered=false.
func (m *Manager) ClusterStatus(id int64) ClusterStatus {
	conn, err := m.getConn(id)
	if err != nil {
		return ClusterStatus{}
	}
	conn.statusMu.RLock()
	defer conn.statusMu.RUnlock()
	st := ClusterStatus{
		Registered:    true,
		Connected:     conn.reachable,
		Synced:        conn.synced,
		ServerVersion: conn.serverVersion,
		LastError:     conn.lastError,
	}
	if !conn.lastCheckedAt.IsZero() {
		t := conn.lastCheckedAt
		st.LastCheckedAt = &t
	}
	return st
}

// DisplayName returns the visible name of a connected cluster, or a fallback
// identifier if it is not found (only for logs/audit).
func (m *Manager) DisplayName(id int64) string {
	conn, err := m.getConn(id)
	if err != nil {
		return fmt.Sprintf("cluster#%d", id)
	}
	return conn.displayName
}

func (m *Manager) getConn(id int64) (*clusterConn, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conns[id]
	if !ok {
		return nil, ErrClusterNotFound
	}
	return c, nil
}

// getSyncedConn is getConn for the reads served from the informer cache.
//
// An unsynced informer has an EMPTY cache, and its listers return zero items
// without error: without this check, an unreachable cluster looks exactly like
// a cluster with no resources ("0 pods, 0 deployments, 0 nodes"), which is the
// confusion all the connection-state work tries to avoid. Better an explicit
// error that the UI can report.
//
// Mutating actions (restart/scale/exec/logs) keep using getConn: they go
// straight to the API server and do not depend on the cache, so there is no
// reason to block them on the informer's state — if the cluster is down, they
// will fail on their own with the real error.
func (m *Manager) getSyncedConn(id int64) (*clusterConn, error) {
	conn, err := m.getConn(id)
	if err != nil {
		return nil, err
	}
	conn.statusMu.RLock()
	synced := conn.synced
	conn.statusMu.RUnlock()
	if !synced {
		return nil, ErrClusterNotSynced
	}
	return conn, nil
}

// getOptionalConn is getSyncedConn plus the check of one specific optional
// informer. If that type has not synced (usually because the cluster's
// ClusterRole does not include the permission), it returns
// ErrInformerNotSynced and ONLY that listing fails.
func (m *Manager) getOptionalConn(id int64, name string) (*clusterConn, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	inf, ok := conn.optional[name]
	if !ok {
		return nil, fmt.Errorf("k8s: optional informer %q not registered", name)
	}
	if !inf.HasSynced() {
		if msg := conn.optionalError(name); msg != "" {
			return nil, fmt.Errorf("%w: %s: %s", ErrInformerNotSynced, name, msg)
		}
		return nil, fmt.Errorf("%w: %s", ErrInformerNotSynced, name)
	}
	return conn, nil
}
