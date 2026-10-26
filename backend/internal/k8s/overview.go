package k8s

import (
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
)

// Overview caps. The "needs attention" list is for acting, not for auditing:
// with 400 broken pods, showing the 50 oldest and saying how many are left out
// is more useful than dumping them all. It is never truncated silently (see
// IssuesTruncated).
const (
	maxOverviewIssues        = 50
	maxOverviewWarningEvents = 10
)

// ResourceCount is the gauge pattern: total plus how
// many are wrong, instead of a health percentage. "340 pods, 3 in error" is
// read at a glance; "99.1% healthy" does not say what to look at.
type ResourceCount struct {
	Total   int `json:"total"`
	Warning int `json:"warning"`
	Error   int `json:"error"`
}

// Capacity holds the capacity numbers that can be computed WITHOUT
// metrics-server: what is reserved (sum of requests of the non-terminated pods)
// against what the nodes have allocatable. That is why the "reserved" ring
// needs no metrics-server (only the "used" one does).
//
// IMPORTANT: this is NOT real usage. A cluster with 90% reserved may be idle,
// and one with 10% reserved may be choking. The UI has to label it as
// "reserved", never as "used".
type Capacity struct {
	PodsUsed               int64 `json:"podsUsed"`
	PodsAllocatable        int64 `json:"podsAllocatable"`
	CPURequestedMilli      int64 `json:"cpuRequestedMilli"`
	CPUAllocatableMilli    int64 `json:"cpuAllocatableMilli"`
	MemoryRequestedBytes   int64 `json:"memoryRequestedBytes"`
	MemoryAllocatableBytes int64 `json:"memoryAllocatableBytes"`
}

// ClusterOverview is everything the cluster overview screen needs, in a single
// request and served entirely from the informer cache.
type ClusterOverview struct {
	// KubeletVersion is the most frequent version among the nodes.
	KubeletVersion string `json:"kubeletVersion"`
	// KubeletVersions is only filled if they are NOT all the same: a half-upgraded
	// cluster is an operational problem worth seeing without looking for it.
	KubeletVersions []string                 `json:"kubeletVersions,omitempty"`
	Namespaces      int                      `json:"namespaces"`
	Counts          map[string]ResourceCount `json:"counts"`
	Capacity        Capacity                 `json:"capacity"`
	Issues          []Issue                  `json:"issues"`
	IssuesTotal     int                      `json:"issuesTotal"`
	WarningEvents   []EventSummary           `json:"warningEvents"`
	// OldestNodeCreatedAt is the closest thing to the cluster's age that can be
	// known from the API: the creation of the oldest node. It is not the real age
	// of the cluster (a node can be replaced), which is why the name says so.
	OldestNodeCreatedAt *time.Time `json:"oldestNodeCreatedAt,omitempty"`
}

// ClusterOverview composes a cluster's overview. Like ClusterSummary, it is a
// pure read of the informer cache: not a single new call to the API server.
//
// It deliberately does NOT accept a namespace filter: it is a cluster view, and
// half the screen (nodes, capacity, versions) makes no sense scoped to a
// namespace.
func (m *Manager) ClusterOverview(id int64, now time.Time) (ClusterOverview, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return ClusterOverview{}, err
	}

	out := ClusterOverview{Counts: map[string]ResourceCount{}}
	var issues []Issue

	pods, err := conn.podLister.Pods("").List(labels.Everything())
	if err != nil {
		return ClusterOverview{}, fmt.Errorf("k8s: failed to list pods for the overview: %w", err)
	}
	podCount := ResourceCount{Total: len(pods)}
	for _, p := range pods {
		if issue := PodIssue(p, now); issue != nil {
			issues = append(issues, *issue)
			countIssue(&podCount, issue.Severity)
		}
	}
	out.Counts["pods"] = podCount

	deployments, err := conn.deploymentLister.Deployments("").List(labels.Everything())
	if err != nil {
		return ClusterOverview{}, fmt.Errorf("k8s: failed to list deployments for the overview: %w", err)
	}
	deploymentCount := ResourceCount{Total: len(deployments)}
	for _, d := range deployments {
		if issue := DeploymentIssue(d, now); issue != nil {
			issues = append(issues, *issue)
			countIssue(&deploymentCount, issue.Severity)
		}
	}
	out.Counts["deployments"] = deploymentCount

	statefulSets, err := conn.statefulSetLister.StatefulSets("").List(labels.Everything())
	if err != nil {
		return ClusterOverview{}, fmt.Errorf("k8s: failed to list statefulsets for the overview: %w", err)
	}
	statefulSetCount := ResourceCount{Total: len(statefulSets)}
	for _, s := range statefulSets {
		if issue := StatefulSetIssue(s, now); issue != nil {
			issues = append(issues, *issue)
			countIssue(&statefulSetCount, issue.Severity)
		}
	}
	out.Counts["statefulsets"] = statefulSetCount

	daemonSets, err := conn.daemonSetLister.DaemonSets("").List(labels.Everything())
	if err != nil {
		return ClusterOverview{}, fmt.Errorf("k8s: failed to list daemonsets for the overview: %w", err)
	}
	daemonSetCount := ResourceCount{Total: len(daemonSets)}
	for _, d := range daemonSets {
		if issue := DaemonSetIssue(d, now); issue != nil {
			issues = append(issues, *issue)
			countIssue(&daemonSetCount, issue.Severity)
		}
	}
	out.Counts["daemonsets"] = daemonSetCount

	nodes, err := conn.nodeLister.List(labels.Everything())
	if err != nil {
		return ClusterOverview{}, fmt.Errorf("k8s: failed to list nodes for the overview: %w", err)
	}
	nodeCount := ResourceCount{Total: len(nodes)}
	for _, n := range nodes {
		if issue := NodeIssue(n, now); issue != nil {
			issues = append(issues, *issue)
			countIssue(&nodeCount, issue.Severity)
		}
	}
	out.Counts["nodes"] = nodeCount

	namespaces, err := conn.namespaceLister.List(labels.Everything())
	if err != nil {
		return ClusterOverview{}, fmt.Errorf("k8s: failed to list namespaces for the overview: %w", err)
	}
	out.Namespaces = len(namespaces)

	out.KubeletVersion, out.KubeletVersions = kubeletVersions(nodes)
	out.OldestNodeCreatedAt = oldestCreation(nodes)
	out.Capacity = clusterCapacity(pods, nodes)

	SortIssues(issues)
	out.IssuesTotal = len(issues)
	if len(issues) > maxOverviewIssues {
		issues = issues[:maxOverviewIssues]
	}
	out.Issues = issues
	if out.Issues == nil {
		// Never null in the JSON: the frontend iterates without checking.
		out.Issues = []Issue{}
	}

	events, err := conn.eventLister.Events("").List(labels.Everything())
	if err != nil {
		return ClusterOverview{}, fmt.Errorf("k8s: failed to list events for the overview: %w", err)
	}
	out.WarningEvents = topWarningEvents(events)

	return out, nil
}

func countIssue(count *ResourceCount, severity Severity) {
	if severity == SeverityError {
		count.Error++
		return
	}
	count.Warning++
}

// kubeletVersions returns the most frequent version and, only if there is more
// than one, the full sorted list.
func kubeletVersions(nodes []*corev1.Node) (string, []string) {
	counts := map[string]int{}
	for _, n := range nodes {
		if v := n.Status.NodeInfo.KubeletVersion; v != "" {
			counts[v]++
		}
	}
	var top string
	var topCount int
	distinct := make([]string, 0, len(counts))
	for v, c := range counts {
		distinct = append(distinct, v)
		// Ties are broken alphabetically so the response is stable across
		// requests (map iteration is not).
		if c > topCount || (c == topCount && v < top) {
			top, topCount = v, c
		}
	}
	if len(distinct) <= 1 {
		return top, nil
	}
	sort.Strings(distinct)
	return top, distinct
}

func oldestCreation(nodes []*corev1.Node) *time.Time {
	var oldest time.Time
	for _, n := range nodes {
		t := n.CreationTimestamp.Time
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	if oldest.IsZero() {
		return nil
	}
	return &oldest
}

// clusterCapacity sums pod requests against node allocatable.
//
// It only counts NON-terminated pods: a Succeeded or Failed pod no longer
// reserves anything on the node, and adding them inflates the number (CronJobs
// leave hundreds of completed pods). It is the same criterion the scheduler
// applies.
func clusterCapacity(pods []*corev1.Pod, nodes []*corev1.Node) Capacity {
	var c Capacity
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		c.PodsUsed++
		// Intermediate variables because MilliValue/Value are pointer methods and
		// the value returned by podRequest is not addressable.
		cpu := podRequest(p, corev1.ResourceCPU)
		memory := podRequest(p, corev1.ResourceMemory)
		c.CPURequestedMilli += cpu.MilliValue()
		c.MemoryRequestedBytes += memory.Value()
	}
	for _, n := range nodes {
		if q, ok := n.Status.Allocatable[corev1.ResourcePods]; ok {
			c.PodsAllocatable += q.Value()
		}
		if q, ok := n.Status.Allocatable[corev1.ResourceCPU]; ok {
			c.CPUAllocatableMilli += q.MilliValue()
		}
		if q, ok := n.Status.Allocatable[corev1.ResourceMemory]; ok {
			c.MemoryAllocatableBytes += q.Value()
		}
	}
	return c
}

// podRequest computes a pod's effective request for a resource: the sum of its
// containers, but at least the maximum of its initContainers, which run before
// and one at a time. It is how the scheduler computes it; adding everything
// would give a higher number than what is really reserved.
func podRequest(pod *corev1.Pod, name corev1.ResourceName) resource.Quantity {
	total := resource.Quantity{}
	for _, c := range pod.Spec.Containers {
		if q, ok := c.Resources.Requests[name]; ok {
			total.Add(q)
		}
	}
	maxInit := resource.Quantity{}
	for _, c := range pod.Spec.InitContainers {
		if q, ok := c.Resources.Requests[name]; ok && q.Cmp(maxInit) > 0 {
			maxInit = q
		}
	}
	if maxInit.Cmp(total) > 0 {
		return maxInit
	}
	return total
}

// topWarningEvents returns the most recent Warning events, already deduplicated
// by the API server itself (each Event carries its `count`), ordered by last
// seen. They are not grouped further here: two different events on the same
// object say different things and merging them would lose the reason.
func topWarningEvents(events []*corev1.Event) []EventSummary {
	out := make([]EventSummary, 0, maxOverviewWarningEvents)
	filtered := make([]*corev1.Event, 0, len(events))
	for _, e := range events {
		if e.Type == corev1.EventTypeWarning {
			filtered = append(filtered, e)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return eventLastSeen(filtered[i]).After(eventLastSeen(filtered[j]))
	})
	for i, e := range filtered {
		if i >= maxOverviewWarningEvents {
			break
		}
		out = append(out, EventSummary{
			Namespace:      e.Namespace,
			Type:           e.Type,
			Reason:         e.Reason,
			Message:        e.Message,
			InvolvedObject: fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
			Count:          e.Count,
			LastSeen:       eventLastSeen(e),
		})
	}
	return out
}
