package k8s

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/client-go/rest"
)

// ConnectorInCluster is the cluster where the server itself runs. The
// `local` cluster: it only shows up when the server is deployed in
// Kubernetes (chart), with the identity of the pod's ServiceAccount, and there
// is nothing to configure and no credential to safeguard.
const ConnectorInCluster = "incluster"

// EnsureLocalCluster registers (once) the in-cluster cluster if the process
// runs inside Kubernetes. Without an in-cluster environment it does nothing:
// in local development or in Compose "this cluster" does not exist. It returns
// the id or 0.
func (m *Manager) EnsureLocalCluster(ctx context.Context, name string) (int64, error) {
	if _, err := rest.InClusterConfig(); err != nil {
		if errors.Is(err, rest.ErrNotInCluster) {
			return 0, nil
		}
		return 0, fmt.Errorf("k8s: invalid in-cluster configuration: %w", err)
	}
	var id int64
	err := m.pool.QueryRow(ctx, `SELECT id FROM clusters WHERE connector = $1`, ConnectorInCluster).Scan(&id)
	if err == nil {
		return id, nil
	}
	if name == "" {
		name = "local"
	}
	// api_endpoint is informational: the real connection is resolved by InClusterConfig.
	err = m.pool.QueryRow(ctx,
		`INSERT INTO clusters (display_name, description, kind, connector, api_endpoint, insecure_skip_tls_verify)
		 VALUES ($1, 'Cluster where the server runs (registered automatically)', 'kubernetes', $2, 'https://kubernetes.default.svc', FALSE) RETURNING id`,
		name, ConnectorInCluster).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("k8s: failed to register the local cluster: %w", err)
	}
	m.logger.Info("local cluster registered", "id", id, "name", name)
	return id, nil
}
