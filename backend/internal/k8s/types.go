// Package k8s centralizes the multi-cluster connection: each managed cluster
// has its own in-memory SharedInformerFactory (reads served from the cache,
// without hitting the API server on every panel request), and its mutating
// actions (restart/scale/logs) go straight to the API server, always
// impersonating the real Bayplan user on top of the technical ServiceAccount's
// identity so Kubernetes' native audit records who acted.
package k8s

import (
	"errors"
	"time"
)

// ErrClusterNotFound is returned when operating on a cluster that is not
// registered or whose connection has not been established (disabled, just
// added and still connecting, or deleted).
var ErrClusterNotFound = errors.New("k8s: cluster not found or not connected")

// ErrClusterNotSynced is returned when the cluster is registered but its
// informer cache has not synced yet (or has stopped being synced). It is
// deliberately distinct from ErrClusterNotFound: here the cluster exists and
// the situation is temporary, so the API responds 503 and not 404.
var ErrClusterNotSynced = errors.New("k8s: the cluster's informer cache is not synced yet")

// ErrAgentOffline: the cluster uses the agent connector and the agent does not
// have the tunnel open right now. It is not a configuration failure: it fixes
// itself when the agent reconnects (the Hub calls Connect then).
var ErrAgentOffline = errors.New("k8s: agent not connected")

// ErrNotKubernetes: the "cluster" is a Docker host and this Manager does not apply to it.
var ErrNotKubernetes = errors.New("k8s: the environment is not a Kubernetes cluster")

// ErrInformerNotSynced is returned when an OPTIONAL informer (ingress, jobs,
// configmaps…) has not synced even though the rest of the cluster has: almost
// always because the managed cluster's ClusterRole does not include that
// permission yet. Only that listing fails, not the whole cluster.
var ErrInformerNotSynced = errors.New("k8s: this resource kind is not available in the cluster (informer not synced)")

// ClusterConfig is the persisted configuration of a managed cluster as read
// from the clusters table (credentials still encrypted). There are two ways to
// authenticate: token (ServiceAccount) or client certificate; at least one is
// present (constraint clusters_has_credential).
type ClusterConfig struct {
	ID          int64
	DisplayName string
	// Kind: "kubernetes" or "docker" (Docker/Podman host via agent; managed by
	// internal/dockerenv, not by this Manager).
	Kind string
	// Connector: "direct" (the server reaches the API server with the
	// credentials below) or "agent" (an agent in the cluster opens an outbound
	// tunnel and the server talks through it; no credentials of its own).
	Connector          string
	APIEndpoint        string
	CACert             string // empty only if Insecure
	Insecure           bool
	SATokenEncrypted   []byte
	ClientCert         string
	ClientKeyEncrypted []byte
	KeyID              string
	// "cloud" connector: the identity comes from a provider credential
	// (internal/cloud) and CloudRef identifies the cluster within the provider.
	// Endpoint and CA are also stored in the columns above, but they are
	// refreshed from the provider on every connection.
	CloudCredentialID *int64
	CloudRef          []byte
}

// ClusterStatus is the connection state returned in /api/clusters. It replaces
// the by-name join with /readyz the frontend used to do.
type ClusterStatus struct {
	// Registered: the Manager has a connection for this cluster (it is enabled).
	Registered bool `json:"registered"`
	// Connected: the last live check against /version responded.
	Connected bool `json:"connected"`
	// Synced: the informer cache has data (reads work).
	Synced        bool       `json:"synced"`
	ServerVersion string     `json:"serverVersion,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
}

// ClusterReadiness exposes the sync state of a cluster's informer cache,
// consumed by /readyz (see internal/api.HealthHandler) so the UI can tell "no
// data yet" from "configuration error".
type ClusterReadiness struct {
	Cluster        string `json:"cluster"`
	InformerSynced bool   `json:"informer_synced"`
	LastError      string `json:"last_error,omitempty"`
}

// ClusterSummary holds aggregated counters for the dashboard's cluster card: a
// pure read of the informer cache (no new calls to the API server), far
// cheaper than listing and counting on the client.
type ClusterSummary struct {
	Pods                int `json:"pods"`
	PodsNotReady        int `json:"podsNotReady"`
	Deployments         int `json:"deployments"`
	DeploymentsNotReady int `json:"deploymentsNotReady"`
	Nodes               int `json:"nodes"`
	NodesNotReady       int `json:"nodesNotReady"`
}

// NamespaceSummary feeds the multi-namespace picker. It carries the creation
// date because it is the signal that lets you tell the team's namespaces from
// the ones the cluster deployment creates, without inventing a classification
// (see the ListNamespaces comment).
type NamespaceSummary struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type PodSummary struct {
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	Status     string    `json:"status"`
	Ready      string    `json:"ready"`
	Restarts   int32     `json:"restarts"`
	Node       string    `json:"node"`
	CreatedAt  time.Time `json:"createdAt"`
	Containers []string  `json:"containers"`
}

type DeploymentSummary struct {
	Name              string    `json:"name"`
	Namespace         string    `json:"namespace"`
	Replicas          int32     `json:"replicas"`
	ReadyReplicas     int32     `json:"readyReplicas"`
	UpdatedReplicas   int32     `json:"updatedReplicas"`
	AvailableReplicas int32     `json:"availableReplicas"`
	CreatedAt         time.Time `json:"createdAt"`
}

type StatefulSetSummary struct {
	Name          string    `json:"name"`
	Namespace     string    `json:"namespace"`
	Replicas      int32     `json:"replicas"`
	ReadyReplicas int32     `json:"readyReplicas"`
	CreatedAt     time.Time `json:"createdAt"`
}

// DaemonSetSummary uses the DaemonSets' own vocabulary: there are no "desired
// replicas" but one pod per eligible node, so what matters is how many nodes
// should have it and on how many it is ready.
type DaemonSetSummary struct {
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	Desired      int32     `json:"desired"`
	Ready        int32     `json:"ready"`
	Available    int32     `json:"available"`
	Misscheduled int32     `json:"misscheduled"`
	CreatedAt    time.Time `json:"createdAt"`
}

type ServiceSummary struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Type      string    `json:"type"`
	ClusterIP string    `json:"clusterIP"`
	Ports     []string  `json:"ports"`
	CreatedAt time.Time `json:"createdAt"`
}

type NodeSummary struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Roles   []string `json:"roles"`
	Version string   `json:"version"`
	// Unschedulable = cordoned (spec.unschedulable): the UI offers uncordon.
	Unschedulable bool      `json:"unschedulable"`
	CreatedAt     time.Time `json:"createdAt"`
}

type EventSummary struct {
	Namespace      string    `json:"namespace"`
	Type           string    `json:"type"`
	Reason         string    `json:"reason"`
	Message        string    `json:"message"`
	InvolvedObject string    `json:"involvedObject"`
	Count          int32     `json:"count"`
	LastSeen       time.Time `json:"lastSeen"`
}
