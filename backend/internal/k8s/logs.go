package k8s

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// StreamPodLogs opens a log stream of a pod's container, impersonating the
// real user: logs may contain sensitive information or PII, and the access
// must be attributed to the specific person in the cluster's audit, not only
// to the technical ServiceAccount. actingGroup determines the real RBAC used
// to authorize the request (GroupAdmins or GroupReadonly, depending on the
// user's role in Bayplan). The caller is responsible for closing the returned
// io.ReadCloser.
func (m *Manager) StreamPodLogs(ctx context.Context, id int64, namespace, podName, container string, follow bool, tailLines int64, actingUser, actingGroup string) (io.ReadCloser, error) {
	// The panel's own pods log the first-startup setup token.
	if err := m.GuardNamespace(id, namespace); err != nil {
		return nil, err
	}
	conn, err := m.getConn(id)
	if err != nil {
		return nil, err
	}
	client, err := conn.actionClientset(actingUser, actingGroup)
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to prepare action client: %w", err)
	}

	opts := &corev1.PodLogOptions{
		Container: container,
		Follow:    follow,
	}
	if tailLines > 0 {
		opts.TailLines = &tailLines
	}

	stream, err := client.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to open log stream for %s/%s: %w", namespace, podName, err)
	}
	return stream, nil
}

// PodLogs returns the last lines of a container's log in one go (no follow),
// for callers that cannot hold a stream open: the HTTP endpoint behind the MCP
// tools and scripts. Same guard and impersonation as StreamPodLogs. limitBytes
// is passed to the API server so a chatty container cannot make the panel read
// megabytes; truncated says whether that limit was hit.
func (m *Manager) PodLogs(ctx context.Context, id int64, namespace, podName, container string, tailLines int64, previous bool, limitBytes int64, actingUser, actingGroup string) (string, bool, error) {
	if err := m.GuardNamespace(id, namespace); err != nil {
		return "", false, err
	}
	conn, err := m.getConn(id)
	if err != nil {
		return "", false, err
	}
	client, err := conn.actionClientset(actingUser, actingGroup)
	if err != nil {
		return "", false, fmt.Errorf("k8s: failed to prepare action client: %w", err)
	}
	opts := &corev1.PodLogOptions{Container: container, Previous: previous, TailLines: &tailLines, LimitBytes: &limitBytes}
	raw, err := client.CoreV1().Pods(namespace).GetLogs(podName, opts).DoRaw(ctx)
	if err != nil {
		// The log subresource is read as plain text, so client-go drops the
		// API's Status body and a 403 comes back as a bare "unknown": say
		// what it almost always means instead.
		if apierrors.IsForbidden(err) {
			return "", false, fmt.Errorf("k8s: the cluster's RBAC does not let group %s read pods/log in %s (is Bayplan's RBAC applied and up to date on this cluster?)", actingGroup, namespace)
		}
		return "", false, fmt.Errorf("k8s: failed to read logs of %s/%s: %w", namespace, podName, err)
	}
	return string(raw), int64(len(raw)) >= limitBytes, nil
}
