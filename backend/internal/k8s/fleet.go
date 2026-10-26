package k8s

import (
	"errors"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/labels"
)

// Cap on problems per cluster in the fleet view. Lower than the one of a
// cluster's overview because here N clusters are mixed and the goal is "what is
// worst right now", not the inventory: for the detail you go into the cluster.
const maxFleetIssuesPerCluster = 10

// ClusterAttention is the summarized state of a cluster for the fleet view. It
// includes neither capacity nor events: in a list of clusters they are not
// read, and computing them would mean walking every pod of every cluster on
// every refresh for nothing.
type ClusterAttention struct {
	ClusterID      int64  `json:"clusterId"`
	Cluster        string `json:"cluster"`
	Connected      bool   `json:"connected"`
	LastError      string `json:"lastError,omitempty"`
	Nodes          int    `json:"nodes"`
	KubeletVersion string `json:"kubeletVersion,omitempty"`
	// Counts is empty if the cluster is not connected: sending zeros would say
	// there is nothing, when what happens is that it is unknown.
	Counts      map[string]ResourceCount `json:"counts,omitempty"`
	Issues      []Issue                  `json:"issues"`
	IssuesTotal int                      `json:"issuesTotal"`
}

// FleetAttention returns the state of the given clusters, each with its
// prioritized problems. It reuses the cluster overview's classification
// (PodIssue/DeploymentIssue/NodeIssue) wholesale, so a change of criterion
// applies to both views at once.
//
// The caller decides the ids after filtering by permissions: this package knows
// nothing about RBAC.
func (m *Manager) FleetAttention(clusters map[int64]string, now time.Time) []ClusterAttention {
	out := make([]ClusterAttention, 0, len(clusters))
	for id, name := range clusters {
		out = append(out, m.clusterAttention(id, name, now))
	}
	// Stable order with the urgent stuff on top: first the clusters with errors,
	// then the ones with warnings, then by name. A disconnected cluster counts
	// as a problem, not as a cluster with nothing to report.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := attentionRank(a), attentionRank(b); ra != rb {
			return ra > rb
		}
		return a.Cluster < b.Cluster
	})
	return out
}

// attentionRank orders by severity: disconnected and with errors weigh the
// same (in both cases there is something to look at right now), then warnings,
// then nothing.
func attentionRank(c ClusterAttention) int {
	if !c.Connected {
		return 2
	}
	for _, i := range c.Issues {
		if i.Severity == SeverityError {
			return 2
		}
	}
	if len(c.Issues) > 0 {
		return 1
	}
	return 0
}

// ClusterIssues returns ALL the problems of a cluster (without the fleet view's
// cap) and whether the cluster is synced. This is what the notification engine
// consumes: a rule cannot miss problem number 11.
func (m *Manager) ClusterIssues(id int64, now time.Time) (issues []Issue, connected bool, lastError string) {
	conn, err := m.getConn(id)
	if err != nil {
		if errors.Is(err, ErrClusterNotFound) {
			return nil, false, ""
		}
		return nil, false, err.Error()
	}
	conn.statusMu.RLock()
	connected = conn.synced && conn.reachable
	lastError = conn.lastError
	conn.statusMu.RUnlock()
	if !connected {
		return nil, false, lastError
	}
	issues, _, _ = conn.collectIssues(now)
	return issues, true, ""
}

// collectIssues walks pods, workloads and nodes of the informer cache applying
// the classification of attention.go. It also returns the per-type counters for
// the fleet and the node list (kubelet version).
func (conn *clusterConn) collectIssues(now time.Time) ([]Issue, map[string]ResourceCount, []*corev1.Node) {
	var issues []Issue
	counts := map[string]ResourceCount{}
	var nodeList []*corev1.Node

	if pods, err := conn.podLister.Pods("").List(labels.Everything()); err == nil {
		c := ResourceCount{Total: len(pods)}
		for _, p := range pods {
			if issue := PodIssue(p, now); issue != nil {
				issues = append(issues, *issue)
				countIssue(&c, issue.Severity)
			}
		}
		counts["pods"] = c
	}

	if deployments, err := conn.deploymentLister.Deployments("").List(labels.Everything()); err == nil {
		c := ResourceCount{Total: len(deployments)}
		for _, d := range deployments {
			if issue := DeploymentIssue(d, now); issue != nil {
				issues = append(issues, *issue)
				countIssue(&c, issue.Severity)
			}
		}
		counts["deployments"] = c
	}

	// StatefulSets and DaemonSets go into the fleet's problems but NOT into
	// `counts`: the fleet table shows pods and nodes, and adding two more columns
	// per workload type would make it unreadable without adding anything the
	// number of problems does not already say.
	if sets, err := conn.statefulSetLister.StatefulSets("").List(labels.Everything()); err == nil {
		for _, s := range sets {
			if issue := StatefulSetIssue(s, now); issue != nil {
				issues = append(issues, *issue)
			}
		}
	}
	if sets, err := conn.daemonSetLister.DaemonSets("").List(labels.Everything()); err == nil {
		for _, d := range sets {
			if issue := DaemonSetIssue(d, now); issue != nil {
				issues = append(issues, *issue)
			}
		}
	}

	if nodes, err := conn.nodeLister.List(labels.Everything()); err == nil {
		c := ResourceCount{Total: len(nodes)}
		for _, n := range nodes {
			if issue := NodeIssue(n, now); issue != nil {
				issues = append(issues, *issue)
				countIssue(&c, issue.Severity)
			}
		}
		counts["nodes"] = c
		nodeList = nodes
	}

	return issues, counts, nodeList
}

func (m *Manager) clusterAttention(id int64, name string, now time.Time) ClusterAttention {
	out := ClusterAttention{ClusterID: id, Cluster: name, Issues: []Issue{}}

	conn, err := m.getConn(id)
	if err != nil {
		// Disabled, just added or unreachable: the row is returned with
		// Connected=false instead of omitted, so the UI can say "not connected"
		// instead of the cluster vanishing from the list.
		if !errors.Is(err, ErrClusterNotFound) {
			out.LastError = err.Error()
		}
		return out
	}

	// Same rule as ClusterIssues: a synced cache of a cluster that no longer
	// answers is a stale picture, and showing it as connected (with its old
	// counts) would read as "it is fine".
	conn.statusMu.RLock()
	out.Connected = conn.synced && conn.reachable
	out.LastError = conn.lastError
	conn.statusMu.RUnlock()
	if !out.Connected {
		return out
	}

	issues, counts, nodes := conn.collectIssues(now)
	if nodes != nil {
		out.Nodes = len(nodes)
		out.KubeletVersion, _ = kubeletVersions(nodes)
	}

	out.Counts = counts
	SortIssues(issues)
	out.IssuesTotal = len(issues)
	if len(issues) > maxFleetIssuesPerCluster {
		issues = issues[:maxFleetIssuesPerCluster]
	}
	if issues != nil {
		out.Issues = issues
	}
	return out
}
