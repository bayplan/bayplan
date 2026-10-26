package k8s

import (
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// listNamespaced runs `list` once per requested namespace and concatenates the
// result. With an empty list it makes a single call with "" (all).
//
// The list is iterated instead of listing the whole cluster and filtering: the
// informer's lister has an index by namespace, so asking for 2 of 29 namespaces
// walks only those 2. Filtering afterwards would always walk all 29.
func listNamespaced[T any](namespaces []string, list func(namespace string) ([]T, error)) ([]T, error) {
	if len(namespaces) == 0 {
		return list("")
	}
	var out []T
	for _, ns := range namespaces {
		items, err := list(ns)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// ListNamespaces returns the cluster's namespaces with their creation date, for
// the UI's multi-namespace picker.
//
// The date is included because it is what lets you find "ours" without
// inventing any heuristic: the namespaces the cluster deployment creates are all
// born in the same minute, and the ones the team adds, days or weeks later.
// Classifying by name or by labels does not work (see the multi-namespace plan),
// and a wrong classification hides resources silently.
func (m *Manager) ListNamespaces(id int64) ([]NamespaceSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	nss, err := conn.namespaceLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list namespaces: %w", err)
	}
	out := make([]NamespaceSummary, 0, len(nss))
	for _, ns := range nss {
		out = append(out, NamespaceSummary{Name: ns.Name, CreatedAt: ns.CreationTimestamp.Time})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListPods lists pods of the given namespaces (empty list = all).
func (m *Manager) ListPods(id int64, namespaces []string) ([]PodSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	pods, err := listNamespaced(namespaces, func(namespace string) ([]*corev1.Pod, error) {
		return conn.podLister.Pods(namespace).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list pods: %w", err)
	}
	out := make([]PodSummary, 0, len(pods))
	for _, p := range pods {
		containers := make([]string, 0, len(p.Spec.Containers))
		for _, c := range p.Spec.Containers {
			containers = append(containers, c.Name)
		}
		out = append(out, PodSummary{
			Name:       p.Name,
			Namespace:  p.Namespace,
			Status:     podStatus(p),
			Ready:      podReady(p),
			Restarts:   podRestarts(p),
			Node:       p.Spec.NodeName,
			CreatedAt:  p.CreationTimestamp.Time,
			Containers: containers,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListDeployments lists deployments of the given namespaces (empty = all).
func (m *Manager) ListDeployments(id int64, namespaces []string) ([]DeploymentSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	deployments, err := listNamespaced(namespaces, func(namespace string) ([]*appsv1.Deployment, error) {
		return conn.deploymentLister.Deployments(namespace).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list deployments: %w", err)
	}
	out := make([]DeploymentSummary, 0, len(deployments))
	for _, d := range deployments {
		var replicas int32
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		out = append(out, DeploymentSummary{
			Name:              d.Name,
			Namespace:         d.Namespace,
			Replicas:          replicas,
			ReadyReplicas:     d.Status.ReadyReplicas,
			UpdatedReplicas:   d.Status.UpdatedReplicas,
			AvailableReplicas: d.Status.AvailableReplicas,
			CreatedAt:         d.CreationTimestamp.Time,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListStatefulSets lists statefulsets of the given namespaces (empty = all).
func (m *Manager) ListStatefulSets(id int64, namespaces []string) ([]StatefulSetSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	sets, err := listNamespaced(namespaces, func(namespace string) ([]*appsv1.StatefulSet, error) {
		return conn.statefulSetLister.StatefulSets(namespace).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list statefulsets: %w", err)
	}
	out := make([]StatefulSetSummary, 0, len(sets))
	for _, s := range sets {
		var replicas int32
		if s.Spec.Replicas != nil {
			replicas = *s.Spec.Replicas
		}
		out = append(out, StatefulSetSummary{
			Name:          s.Name,
			Namespace:     s.Namespace,
			Replicas:      replicas,
			ReadyReplicas: s.Status.ReadyReplicas,
			CreatedAt:     s.CreationTimestamp.Time,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListDaemonSets lists daemonsets of the given namespaces (empty = all).
func (m *Manager) ListDaemonSets(id int64, namespaces []string) ([]DaemonSetSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	sets, err := listNamespaced(namespaces, func(namespace string) ([]*appsv1.DaemonSet, error) {
		return conn.daemonSetLister.DaemonSets(namespace).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list daemonsets: %w", err)
	}
	out := make([]DaemonSetSummary, 0, len(sets))
	for _, d := range sets {
		out = append(out, DaemonSetSummary{
			Name:         d.Name,
			Namespace:    d.Namespace,
			Desired:      d.Status.DesiredNumberScheduled,
			Ready:        d.Status.NumberReady,
			Available:    d.Status.NumberAvailable,
			Misscheduled: d.Status.NumberMisscheduled,
			CreatedAt:    d.CreationTimestamp.Time,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListDeploymentPods returns a deployment's pods.
//
// In Kubernetes a Deployment does not own its pods directly: the path is
// Deployment -> ReplicaSet -> Pod, through ownerReferences. That is why the
// ReplicaSet informer is needed, which feeds no view of its own.
//
// Old ReplicaSets are included (those of previous revisions, which the
// deployment keeps to be able to roll back): during a rollout pods of two
// revisions coexist and hiding some would give an incomplete list right when it
// matters most to look at it.
func (m *Manager) ListDeploymentPods(id int64, namespace, name string) ([]PodSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}

	deployment, err := conn.deploymentLister.Deployments(namespace).Get(name)
	if err != nil {
		return nil, ErrResourceNotFound
	}

	replicaSets, err := conn.replicaSetLister.ReplicaSets(namespace).List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list replicasets: %w", err)
	}
	ownedRS := map[types.UID]bool{}
	for _, rs := range replicaSets {
		for _, owner := range rs.OwnerReferences {
			if owner.UID == deployment.UID {
				ownedRS[rs.UID] = true
				break
			}
		}
	}

	pods, err := conn.podLister.Pods(namespace).List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list pods: %w", err)
	}
	out := make([]PodSummary, 0)
	for _, p := range pods {
		owned := false
		for _, owner := range p.OwnerReferences {
			if ownedRS[owner.UID] {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		containers := make([]string, 0, len(p.Spec.Containers))
		for _, c := range p.Spec.Containers {
			containers = append(containers, c.Name)
		}
		out = append(out, PodSummary{
			Name:       p.Name,
			Namespace:  p.Namespace,
			Status:     podStatus(p),
			Ready:      podReady(p),
			Restarts:   podRestarts(p),
			Node:       p.Spec.NodeName,
			CreatedAt:  p.CreationTimestamp.Time,
			Containers: containers,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListServices lists services of the given namespaces (empty = all).
func (m *Manager) ListServices(id int64, namespaces []string) ([]ServiceSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	services, err := listNamespaced(namespaces, func(namespace string) ([]*corev1.Service, error) {
		return conn.serviceLister.Services(namespace).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list services: %w", err)
	}
	out := make([]ServiceSummary, 0, len(services))
	for _, s := range services {
		ports := make([]string, 0, len(s.Spec.Ports))
		for _, p := range s.Spec.Ports {
			ports = append(ports, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
		}
		out = append(out, ServiceSummary{
			Name:      s.Name,
			Namespace: s.Namespace,
			Type:      string(s.Spec.Type),
			ClusterIP: s.Spec.ClusterIP,
			Ports:     ports,
			CreatedAt: s.CreationTimestamp.Time,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListNodes lists the cluster's nodes (non-namespaced resource).
func (m *Manager) ListNodes(id int64) ([]NodeSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	nodes, err := conn.nodeLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list nodes: %w", err)
	}
	out := make([]NodeSummary, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, NodeSummary{
			Name:          n.Name,
			Status:        nodeStatus(n),
			Roles:         nodeRoles(n),
			Version:       n.Status.NodeInfo.KubeletVersion,
			Unschedulable: n.Spec.Unschedulable,
			CreatedAt:     n.CreationTimestamp.Time,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// EventFilter narrows which events ListEvents returns. Zero-valued fields are
// ignored (no filter on that dimension). Filtering is applied BEFORE sorting
// and truncating to maxEvents: the other way round (truncate first) a filter by
// type/date could silently lose older matches that had already fallen outside
// the cut of the N most recent.
type EventFilter struct {
	Type  string    // "Normal" or "Warning"; empty = all
	Since time.Time // LastSeen >= Since if not zero
	Until time.Time // LastSeen <= Until if not zero
}

// EventsResult wraps the filtered list together with whether it was truncated
// to maxEvents — the UI needs to know so it does not imply it has seen the
// whole history when in fact more events match the filter than fit in the
// response.
type EventsResult struct {
	Items     []EventSummary
	Truncated bool
}

// ListEvents lists the most recent events of a namespace (empty namespace =
// all) that match filter, limited to maxEvents so as not to overload the UI.
func (m *Manager) ListEvents(id int64, namespaces []string, filter EventFilter, maxEvents int) (EventsResult, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return EventsResult{}, err
	}
	events, err := listNamespaced(namespaces, func(namespace string) ([]*corev1.Event, error) {
		return conn.eventLister.Events(namespace).List(labels.Everything())
	})
	if err != nil {
		return EventsResult{}, fmt.Errorf("k8s: failed to list events: %w", err)
	}
	out := make([]EventSummary, 0, len(events))
	for _, e := range events {
		lastSeen := eventLastSeen(e)
		if filter.Type != "" && e.Type != filter.Type {
			continue
		}
		if !filter.Since.IsZero() && lastSeen.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && lastSeen.After(filter.Until) {
			continue
		}
		out = append(out, EventSummary{
			Namespace:      e.Namespace,
			Type:           e.Type,
			Reason:         e.Reason,
			Message:        e.Message,
			InvolvedObject: fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
			Count:          e.Count,
			LastSeen:       lastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	truncated := false
	if maxEvents > 0 && len(out) > maxEvents {
		out = out[:maxEvents]
		truncated = true
	}
	return EventsResult{Items: out, Truncated: truncated}, nil
}

// ClusterSummary counts pods/deployments/nodes and how many of each are not
// healthy, for the dashboard's aggregated card. It reuses the same listers as
// the List* functions, without any new call to the API server: it is a pure
// read of the already existing informer cache.
func (m *Manager) ClusterSummary(id int64) (ClusterSummary, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return ClusterSummary{}, err
	}

	var s ClusterSummary

	pods, err := conn.podLister.Pods("").List(labels.Everything())
	if err != nil {
		return ClusterSummary{}, fmt.Errorf("k8s: failed to list pods for the summary: %w", err)
	}
	s.Pods = len(pods)
	for _, p := range pods {
		if status := podStatus(p); status != "Running" && status != "Succeeded" {
			s.PodsNotReady++
		}
	}

	deployments, err := conn.deploymentLister.Deployments("").List(labels.Everything())
	if err != nil {
		return ClusterSummary{}, fmt.Errorf("k8s: failed to list deployments for the summary: %w", err)
	}
	s.Deployments = len(deployments)
	for _, d := range deployments {
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		if d.Status.ReadyReplicas < want {
			s.DeploymentsNotReady++
		}
	}

	nodes, err := conn.nodeLister.List(labels.Everything())
	if err != nil {
		return ClusterSummary{}, fmt.Errorf("k8s: failed to list nodes for the summary: %w", err)
	}
	s.Nodes = len(nodes)
	for _, n := range nodes {
		if nodeStatus(n) != "Ready" {
			s.NodesNotReady++
		}
	}

	return s, nil
}

// eventLastSeen normalizes an Event's timestamp: those emitted by the old API
// (core/v1) fill LastTimestamp and those from events.k8s.io use EventTime, so
// both have to be checked or half the events show up with a zero date.
func eventLastSeen(e *corev1.Event) time.Time {
	if !e.LastTimestamp.Time.IsZero() {
		return e.LastTimestamp.Time
	}
	return e.EventTime.Time
}

func podStatus(pod *corev1.Pod) string {
	if pod.DeletionTimestamp != nil {
		return "Terminating"
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	return string(pod.Status.Phase)
}

func podReady(pod *corev1.Pod) string {
	ready := 0
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, len(pod.Status.ContainerStatuses))
}

func podRestarts(pod *corev1.Pod) int32 {
	var total int32
	for _, cs := range pod.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	return total
}

func nodeStatus(n *corev1.Node) string {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			if cond.Status == corev1.ConditionTrue {
				return "Ready"
			}
			return "NotReady"
		}
	}
	return "Unknown"
}

func nodeRoles(n *corev1.Node) []string {
	const rolePrefix = "node-role.kubernetes.io/"
	var roles []string
	for label := range n.Labels {
		if len(label) > len(rolePrefix) && label[:len(rolePrefix)] == rolePrefix {
			roles = append(roles, label[len(rolePrefix):])
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		roles = []string{"worker"}
	}
	return roles
}
