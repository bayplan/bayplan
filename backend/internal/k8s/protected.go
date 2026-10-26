package k8s

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrProtectedNamespace: in the local cluster, the namespace where the panel
// itself runs (its Secrets carry the encryption key and the DB password; its
// Deployment IS the panel). A panel admin cannot read those values, write there
// or exec into those pods from the panel: if they could, "panel admin" and
// "cluster admin" would be the same thing through the back door. It is an
// application guard; the API server one is set by the chart with a
// ValidatingAdmissionPolicy for writes (reads do not go through admission,
// which is why this one is needed).
var ErrProtectedNamespace = errors.New("protected namespace: it is where the panel itself runs")

// ownNamespace is the server pod's namespace (POD_NAMESPACE via downward API,
// or the ServiceAccount file). Empty outside Kubernetes.
var ownNamespace = func() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}()

// AgentNamespace is where the agent manifest installs the agent (the copy in
// internal/agent; a test keeps the two in step). Its Secret carries the agent
// key and its ServiceAccount is the one the panel reads and impersonates
// through: a workload an editor runs under that SA would be the cluster
// admin the panel's own RBAC never grants, so it gets the same guard as the
// panel's namespace in the local cluster.
const AgentNamespace = "bayplan-agent"

// ProtectedNamespace returns a cluster's protected namespace ("" if none):
// the server's own namespace in the in-cluster cluster, the agent's in a
// cluster reached through the agent.
func (m *Manager) ProtectedNamespace(id int64) string {
	conn, err := m.getConn(id)
	if err != nil {
		return ""
	}
	switch {
	case conn.inCluster && ownNamespace != "":
		return ownNamespace
	case conn.viaAgent:
		return AgentNamespace
	}
	return ""
}

// GuardNamespace fails if ns is the cluster's protected one.
func (m *Manager) GuardNamespace(id int64, ns string) error {
	if p := m.ProtectedNamespace(id); p != "" && ns == p {
		return fmt.Errorf("%w (%s)", ErrProtectedNamespace, p)
	}
	return nil
}
