package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
)

// REAL CPU and memory usage via metrics-server (metrics.k8s.io API). It is the
// only thing in the panel that does not come from the informer cache:
// metrics.k8s.io does not support watch, so it is polled every metricsInterval
// and the latest snapshot is kept in memory; every read serves from that
// snapshot. metrics-server is optional in a cluster: if it is absent (404) or
// the SA lacks permission (403), the cluster keeps working and the UI says why
// there is no usage, just like with the optional informers.
//
// The API is spoken raw (two GETs and JSON) instead of importing
// k8s.io/metrics: half a module for two endpoints.

// metricsInterval: metrics-server aggregates every 15-60 s depending on the
// version; polling more often only returns the same sample.
const metricsInterval = 30 * time.Second

type usage struct {
	CPUMilli    int64
	MemoryBytes int64
}

type podUsageSample struct {
	usage
	Containers map[string]usage
}

// metricsSnapshot is a cluster's latest metrics-server snapshot.
type metricsSnapshot struct {
	Available  bool
	ReasonCode string
	Reason     string
	UpdatedAt  time.Time
	Nodes      map[string]usage
	Pods       map[string]podUsageSample // namespace/name
}

type metricsState struct {
	mu   sync.RWMutex
	snap metricsSnapshot
}

func (s *metricsState) get() metricsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

func (s *metricsState) set(snap metricsSnapshot) {
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
}

// metricsLoop polls metrics.k8s.io until stopCh is closed.
func (m *Manager) metricsLoop(conn *clusterConn) {
	fetch := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		snap, err := fetchMetrics(ctx, conn)
		if err != nil {
			prev := conn.metrics.get()
			code, msg := describeMetricsError(err)
			conn.metrics.set(metricsSnapshot{Available: false, ReasonCode: code, Reason: msg, UpdatedAt: prev.UpdatedAt})
			return
		}
		conn.metrics.set(snap)
	}
	fetch()
	ticker := time.NewTicker(metricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-conn.stopCh:
			return
		case <-ticker.C:
			fetch()
		}
	}
}

// Stable codes for why there are no metrics (the UI decides what to offer:
// NotInstalled → install button).
const (
	MetricsNotInstalled = "NotInstalled"
	MetricsForbidden    = "Forbidden"
	MetricsUnavailable  = "Unavailable"
	MetricsError        = "Error"
)

func describeMetricsError(err error) (code, msg string) {
	switch {
	case apierrors.IsNotFound(err):
		return MetricsNotInstalled, "metrics-server is not installed in the cluster (metrics.k8s.io not available)"
	case apierrors.IsForbidden(err):
		return MetricsForbidden, "no permission to read metrics.k8s.io: re-apply deploy/rbac/serviceaccount.yaml in the cluster"
	case apierrors.IsServiceUnavailable(err):
		return MetricsUnavailable, "metrics-server is not responding (APIService metrics.k8s.io unavailable)"
	}
	return MetricsError, describeAPIError(err)
}

type metricsList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Timestamp  time.Time         `json:"timestamp"`
		Usage      map[string]string `json:"usage"`
		Containers []struct {
			Name  string            `json:"name"`
			Usage map[string]string `json:"usage"`
		} `json:"containers"`
	} `json:"items"`
}

func parseUsage(u map[string]string) usage {
	var out usage
	if q, err := resource.ParseQuantity(u["cpu"]); err == nil {
		out.CPUMilli = q.MilliValue()
	}
	if q, err := resource.ParseQuantity(u["memory"]); err == nil {
		out.MemoryBytes = q.Value()
	}
	return out
}

func fetchMetrics(ctx context.Context, conn *clusterConn) (metricsSnapshot, error) {
	rc := conn.clientset.Discovery().RESTClient()
	var nodes, pods metricsList
	raw, err := rc.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").Do(ctx).Raw()
	if err != nil {
		return metricsSnapshot{}, err
	}
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return metricsSnapshot{}, fmt.Errorf("invalid metrics-server response: %w", err)
	}
	raw, err = rc.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/pods").Do(ctx).Raw()
	if err != nil {
		return metricsSnapshot{}, err
	}
	if err := json.Unmarshal(raw, &pods); err != nil {
		return metricsSnapshot{}, fmt.Errorf("invalid metrics-server response: %w", err)
	}
	snap := metricsSnapshot{Available: true, UpdatedAt: time.Now(), Nodes: map[string]usage{}, Pods: map[string]podUsageSample{}}
	for _, n := range nodes.Items {
		snap.Nodes[n.Metadata.Name] = parseUsage(n.Usage)
	}
	for _, p := range pods.Items {
		sample := podUsageSample{Containers: map[string]usage{}}
		for _, c := range p.Containers {
			u := parseUsage(c.Usage)
			sample.Containers[c.Name] = u
			sample.CPUMilli += u.CPUMilli
			sample.MemoryBytes += u.MemoryBytes
		}
		snap.Pods[p.Metadata.Namespace+"/"+p.Metadata.Name] = sample
	}
	return snap, nil
}

// NodeUsage is a node's usage next to what is reserved and what is
// allocatable, so the UI can draw the three on the same scale.
type NodeUsage struct {
	Name                   string `json:"name,omitempty"`
	CPUUsedMilli           int64  `json:"cpuUsedMilli"`
	CPURequestedMilli      int64  `json:"cpuRequestedMilli"`
	CPUAllocatableMilli    int64  `json:"cpuAllocatableMilli"`
	MemoryUsedBytes        int64  `json:"memoryUsedBytes"`
	MemoryRequestedBytes   int64  `json:"memoryRequestedBytes"`
	MemoryAllocatableBytes int64  `json:"memoryAllocatableBytes"`
	// HasMetrics: metrics-server has a sample for this node (a freshly joined
	// node takes a minute to show up).
	HasMetrics bool `json:"hasMetrics"`
}

// ClusterUsage is the response of GET /api/clusters/{id}/metrics.
type ClusterUsage struct {
	Available  bool        `json:"available"`
	ReasonCode string      `json:"reasonCode,omitempty"`
	Reason     string      `json:"reason,omitempty"`
	UpdatedAt  *time.Time  `json:"updatedAt,omitempty"`
	Totals     NodeUsage   `json:"totals"`
	Nodes      []NodeUsage `json:"nodes"`
}

// ClusterUsage joins the metrics-server snapshot with nodes and pods from the
// informer cache (allocatable and requests per node).
func (m *Manager) ClusterUsage(id int64) (ClusterUsage, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return ClusterUsage{}, err
	}
	snap := conn.metrics.get()
	out := ClusterUsage{Available: snap.Available, ReasonCode: snap.ReasonCode, Reason: snap.Reason, Nodes: []NodeUsage{}}
	if !snap.UpdatedAt.IsZero() {
		t := snap.UpdatedAt
		out.UpdatedAt = &t
	}
	nodes, err := conn.nodeLister.List(labels.Everything())
	if err != nil {
		return ClusterUsage{}, err
	}
	pods, err := conn.podLister.Pods("").List(labels.Everything())
	if err != nil {
		return ClusterUsage{}, err
	}
	requested := map[string]usage{}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed || p.Spec.NodeName == "" {
			continue
		}
		cpu := podRequest(p, corev1.ResourceCPU)
		mem := podRequest(p, corev1.ResourceMemory)
		r := requested[p.Spec.NodeName]
		r.CPUMilli += cpu.MilliValue()
		r.MemoryBytes += mem.Value()
		requested[p.Spec.NodeName] = r
	}
	for _, n := range nodes {
		nu := NodeUsage{Name: n.Name}
		if q, ok := n.Status.Allocatable[corev1.ResourceCPU]; ok {
			nu.CPUAllocatableMilli = q.MilliValue()
		}
		if q, ok := n.Status.Allocatable[corev1.ResourceMemory]; ok {
			nu.MemoryAllocatableBytes = q.Value()
		}
		nu.CPURequestedMilli = requested[n.Name].CPUMilli
		nu.MemoryRequestedBytes = requested[n.Name].MemoryBytes
		if u, ok := snap.Nodes[n.Name]; ok {
			nu.HasMetrics = true
			nu.CPUUsedMilli = u.CPUMilli
			nu.MemoryUsedBytes = u.MemoryBytes
		}
		out.Totals.CPUUsedMilli += nu.CPUUsedMilli
		out.Totals.CPURequestedMilli += nu.CPURequestedMilli
		out.Totals.CPUAllocatableMilli += nu.CPUAllocatableMilli
		out.Totals.MemoryUsedBytes += nu.MemoryUsedBytes
		out.Totals.MemoryRequestedBytes += nu.MemoryRequestedBytes
		out.Totals.MemoryAllocatableBytes += nu.MemoryAllocatableBytes
		out.Nodes = append(out.Nodes, nu)
	}
	out.Totals.HasMetrics = snap.Available
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Name < out.Nodes[j].Name })
	return out, nil
}

// ContainerUsage is a container's usage against its request and limit.
type ContainerUsage struct {
	Name               string `json:"name"`
	CPUUsedMilli       int64  `json:"cpuUsedMilli"`
	CPURequestMilli    int64  `json:"cpuRequestMilli"`
	CPULimitMilli      int64  `json:"cpuLimitMilli"`
	MemoryUsedBytes    int64  `json:"memoryUsedBytes"`
	MemoryRequestBytes int64  `json:"memoryRequestBytes"`
	MemoryLimitBytes   int64  `json:"memoryLimitBytes"`
}

// PodUsage is a row of GET /api/clusters/{id}/metrics/pods.
type PodUsage struct {
	Namespace          string           `json:"namespace"`
	Name               string           `json:"name"`
	Node               string           `json:"node"`
	HasMetrics         bool             `json:"hasMetrics"`
	CPUUsedMilli       int64            `json:"cpuUsedMilli"`
	CPURequestMilli    int64            `json:"cpuRequestMilli"`
	CPULimitMilli      int64            `json:"cpuLimitMilli"`
	MemoryUsedBytes    int64            `json:"memoryUsedBytes"`
	MemoryRequestBytes int64            `json:"memoryRequestBytes"`
	MemoryLimitBytes   int64            `json:"memoryLimitBytes"`
	Containers         []ContainerUsage `json:"containers"`
}

// PodUsageList is the full response: availability + rows.
type PodUsageList struct {
	Available bool       `json:"available"`
	Reason    string     `json:"reason,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	Pods      []PodUsage `json:"pods"`
}

// PodUsage joins the metrics-server snapshot with the (non-terminated) pods of
// the informer cache in the given namespaces (empty = all).
func (m *Manager) PodUsage(id int64, namespaces []string) (PodUsageList, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return PodUsageList{}, err
	}
	snap := conn.metrics.get()
	out := PodUsageList{Available: snap.Available, Reason: snap.Reason, Pods: []PodUsage{}}
	if !snap.UpdatedAt.IsZero() {
		t := snap.UpdatedAt
		out.UpdatedAt = &t
	}
	var pods []*corev1.Pod
	if len(namespaces) == 0 {
		pods, err = conn.podLister.Pods("").List(labels.Everything())
		if err != nil {
			return PodUsageList{}, err
		}
	} else {
		for _, ns := range namespaces {
			list, err := conn.podLister.Pods(ns).List(labels.Everything())
			if err != nil {
				return PodUsageList{}, err
			}
			pods = append(pods, list...)
		}
	}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		row := PodUsage{Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName, Containers: []ContainerUsage{}}
		sample, ok := snap.Pods[p.Namespace+"/"+p.Name]
		row.HasMetrics = ok
		for _, c := range p.Spec.Containers {
			cu := ContainerUsage{Name: c.Name}
			if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				cu.CPURequestMilli = q.MilliValue()
			}
			if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
				cu.CPULimitMilli = q.MilliValue()
			}
			if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				cu.MemoryRequestBytes = q.Value()
			}
			if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
				cu.MemoryLimitBytes = q.Value()
			}
			if u, ok := sample.Containers[c.Name]; ok {
				cu.CPUUsedMilli = u.CPUMilli
				cu.MemoryUsedBytes = u.MemoryBytes
			}
			row.CPURequestMilli += cu.CPURequestMilli
			row.CPULimitMilli += cu.CPULimitMilli
			row.MemoryRequestBytes += cu.MemoryRequestBytes
			row.MemoryLimitBytes += cu.MemoryLimitBytes
			row.CPUUsedMilli += cu.CPUUsedMilli
			row.MemoryUsedBytes += cu.MemoryUsedBytes
			row.Containers = append(row.Containers, cu)
		}
		out.Pods = append(out.Pods, row)
	}
	sort.Slice(out.Pods, func(i, j int) bool {
		if out.Pods[i].Namespace != out.Pods[j].Namespace {
			return out.Pods[i].Namespace < out.Pods[j].Namespace
		}
		return out.Pods[i].Name < out.Pods[j].Name
	})
	return out, nil
}
