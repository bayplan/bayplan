package k8s

import (
	"errors"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// maxTopologyNodes bounds what is sent to the browser. Beyond a few thousand
// nodes the graph is unreadable and the layout freezes the tab, so instead of
// cutting it (a graph with half its edges lies about what depends on what) the
// response comes back empty with TooLarge and the UI asks for a namespace filter.
const maxTopologyNodes = 3000

// Topology statuses. "ok" and not an absent field: the UI paints every node and
// an unknown value must fall into amber, like lib/status.ts does.
const (
	TopologyOK      = "ok"
	TopologyWarning = "warning"
	TopologyError   = "error"
)

// Edge kinds. They are what the API server knows (configuration), never
// observed traffic: that needs a CNI/mesh that sees packets and would be a
// different, clearly labeled kind.
const (
	EdgeRoutes  = "routes"  // Ingress -> Service
	EdgeSelects = "selects" // Service -> workload or bare pod
	EdgeOwns    = "owns"    // workload -> pod
	EdgeMounts  = "mounts"  // pod -> PVC
)

type TopologyNode struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	// Reason is a stable code (the attention.go ones plus NoEndpoints,
	// BackendNotFound, ClaimPending, ClaimLost), translated by the UI.
	Reason string `json:"reason,omitempty"`
	// Detail carries the numbers ("2/3", "Running", "20Gi"), not prose.
	Detail string `json:"detail,omitempty"`
	// Owner is the workload node of a pod, so the UI can fold pods into it.
	Owner string `json:"owner,omitempty"`
	// NodeName is the Kubernetes node a pod runs on.
	NodeName string `json:"nodeName,omitempty"`
	// Config lists the ConfigMaps and Secrets a workload's pod template
	// references ("ConfigMap/name"). Names only: values never travel here.
	Config []string `json:"config,omitempty"`
}

type TopologyEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

type Topology struct {
	Nodes []TopologyNode `json:"nodes"`
	Edges []TopologyEdge `json:"edges"`
	// Total is the node count before the TooLarge check.
	Total    int  `json:"total"`
	TooLarge bool `json:"tooLarge,omitempty"`
	// Unavailable maps an optional kind left out of the graph (no permission,
	// not synced) to the reason, so missing Ingresses are not read as "none".
	Unavailable map[string]string `json:"unavailable,omitempty"`
}

// TopologyInput is what BuildTopology works on: the objects exactly as they
// sit in the informer cache, already filtered by namespace.
type TopologyInput struct {
	Pods         []*corev1.Pod
	Deployments  []*appsv1.Deployment
	StatefulSets []*appsv1.StatefulSet
	DaemonSets   []*appsv1.DaemonSet
	ReplicaSets  []*appsv1.ReplicaSet
	Services     []*corev1.Service
	Ingresses    []*networkingv1.Ingress
	PVCs         []*corev1.PersistentVolumeClaim
}

func topoID(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

// workloadTemplate is what a Service selector and the config references are
// matched against: the pod template, so a workload scaled to 0 keeps its edges.
type workloadTemplate struct {
	id       string
	ns       string
	template corev1.PodTemplateSpec
}

// BuildTopology turns the cached objects into the graph. Pure: the clock is a
// parameter because statuses reuse the attention.go grace periods, which is
// what keeps the graph from being red during every normal rollout.
func BuildTopology(in TopologyInput, now time.Time) Topology {
	out := Topology{Nodes: []TopologyNode{}, Edges: []TopologyEdge{}}
	nodes := map[string]int{} // id -> index in out.Nodes
	addNode := func(n TopologyNode) {
		nodes[n.ID] = len(out.Nodes)
		out.Nodes = append(out.Nodes, n)
	}
	edgeSeen := map[TopologyEdge]bool{}
	addEdge := func(source, target, kind string) {
		e := TopologyEdge{Source: source, Target: target, Kind: kind}
		if _, ok := nodes[source]; !ok || edgeSeen[e] {
			return
		}
		if _, ok := nodes[target]; !ok {
			return
		}
		edgeSeen[e] = true
		out.Edges = append(out.Edges, e)
	}

	var templates []workloadTemplate
	for _, d := range in.Deployments {
		n := TopologyNode{ID: topoID("Deployment", d.Namespace, d.Name), Kind: "Deployment", Namespace: d.Namespace, Name: d.Name, Status: TopologyOK}
		n.Detail = fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, replicasOrOne(d.Spec.Replicas))
		applyIssue(&n, DeploymentIssue(d, now))
		n.Config = templateConfig(d.Spec.Template.Spec)
		addNode(n)
		templates = append(templates, workloadTemplate{n.ID, d.Namespace, d.Spec.Template})
	}
	for _, s := range in.StatefulSets {
		n := TopologyNode{ID: topoID("StatefulSet", s.Namespace, s.Name), Kind: "StatefulSet", Namespace: s.Namespace, Name: s.Name, Status: TopologyOK}
		n.Detail = fmt.Sprintf("%d/%d", s.Status.ReadyReplicas, replicasOrOne(s.Spec.Replicas))
		applyIssue(&n, StatefulSetIssue(s, now))
		n.Config = templateConfig(s.Spec.Template.Spec)
		addNode(n)
		templates = append(templates, workloadTemplate{n.ID, s.Namespace, s.Spec.Template})
	}
	for _, d := range in.DaemonSets {
		n := TopologyNode{ID: topoID("DaemonSet", d.Namespace, d.Name), Kind: "DaemonSet", Namespace: d.Namespace, Name: d.Name, Status: TopologyOK}
		n.Detail = fmt.Sprintf("%d/%d", d.Status.NumberReady, d.Status.DesiredNumberScheduled)
		applyIssue(&n, DaemonSetIssue(d, now))
		n.Config = templateConfig(d.Spec.Template.Spec)
		addNode(n)
		templates = append(templates, workloadTemplate{n.ID, d.Namespace, d.Spec.Template})
	}

	// A ReplicaSet owned by a Deployment is an implementation detail and is
	// folded into it; only a bare one (created by hand or by an operator)
	// becomes a node, otherwise its pods would look orphaned.
	rsOwner := map[string]string{} // ns/name -> owning node id
	for _, rs := range in.ReplicaSets {
		owner := ""
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" {
				owner = topoID("Deployment", rs.Namespace, ref.Name)
			}
		}
		if owner != "" {
			rsOwner[rs.Namespace+"/"+rs.Name] = owner
			continue
		}
		if replicasOrOne(rs.Spec.Replicas) == 0 {
			continue
		}
		n := TopologyNode{ID: topoID("ReplicaSet", rs.Namespace, rs.Name), Kind: "ReplicaSet", Namespace: rs.Namespace, Name: rs.Name, Status: TopologyOK}
		n.Detail = fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, replicasOrOne(rs.Spec.Replicas))
		n.Config = templateConfig(rs.Spec.Template.Spec)
		addNode(n)
		rsOwner[rs.Namespace+"/"+rs.Name] = n.ID
		templates = append(templates, workloadTemplate{n.ID, rs.Namespace, rs.Spec.Template})
	}

	for _, p := range in.PVCs {
		n := TopologyNode{ID: topoID("PersistentVolumeClaim", p.Namespace, p.Name), Kind: "PersistentVolumeClaim", Namespace: p.Namespace, Name: p.Name, Status: TopologyOK}
		if q, ok := p.Status.Capacity[corev1.ResourceStorage]; ok {
			n.Detail = q.String()
		}
		switch p.Status.Phase {
		case corev1.ClaimLost:
			n.Status, n.Reason = TopologyError, "ClaimLost"
		case corev1.ClaimPending:
			// A WaitForFirstConsumer claim is Pending until its pod is
			// scheduled: only a long wait is a problem.
			if now.Sub(p.CreationTimestamp.Time) >= podGrace {
				n.Status, n.Reason = TopologyWarning, "ClaimPending"
			}
		}
		addNode(n)
	}

	// Pods. Succeeded ones are left out: they serve nothing and CronJobs leave
	// hundreds behind.
	type podInfo struct {
		pod   *corev1.Pod
		id    string
		ready bool
		bad   bool
	}
	var pods []podInfo
	for _, p := range in.Pods {
		if p.Status.Phase == corev1.PodSucceeded {
			continue
		}
		n := TopologyNode{ID: topoID("Pod", p.Namespace, p.Name), Kind: "Pod", Namespace: p.Namespace, Name: p.Name, Status: TopologyOK, NodeName: p.Spec.NodeName}
		n.Detail = string(p.Status.Phase)
		if p.DeletionTimestamp != nil {
			n.Detail = "Terminating"
		}
		issue := PodIssue(p, now)
		applyIssue(&n, issue)
		n.Owner = podOwnerID(p, rsOwner)
		if _, ok := nodes[n.Owner]; !ok {
			n.Owner = ""
		}
		addNode(n)
		pods = append(pods, podInfo{pod: p, id: n.ID, ready: podIsReady(p), bad: issue != nil})
		if n.Owner != "" {
			addEdge(n.Owner, n.ID, EdgeOwns)
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				addEdge(n.ID, topoID("PersistentVolumeClaim", p.Namespace, v.PersistentVolumeClaim.ClaimName), EdgeMounts)
			}
		}
	}

	// Services select pods, but the edge goes to the workload when the pod has
	// one: one line per Deployment instead of one per replica. The workload
	// match uses the template so the edge survives a scale to 0; the endpoint
	// count uses the real pods.
	serviceIDs := map[string]bool{}
	for _, s := range in.Services {
		n := TopologyNode{ID: topoID("Service", s.Namespace, s.Name), Kind: "Service", Namespace: s.Namespace, Name: s.Name, Status: TopologyOK}
		serviceIDs[n.ID] = true
		addNode(n)
		if len(s.Spec.Selector) == 0 {
			// ExternalName or manual Endpoints: nothing to resolve from the cache.
			continue
		}
		sel := labels.SelectorFromSet(s.Spec.Selector)
		for _, w := range templates {
			if w.ns == s.Namespace && sel.Matches(labels.Set(w.template.Labels)) {
				addEdge(n.ID, w.id, EdgeSelects)
			}
		}
		total, ready, bad := 0, 0, 0
		for _, pi := range pods {
			if pi.pod.Namespace != s.Namespace || !sel.Matches(labels.Set(pi.pod.Labels)) {
				continue
			}
			total++
			if pi.ready {
				ready++
			}
			if pi.bad {
				bad++
			}
			if out.Nodes[nodes[pi.id]].Owner == "" {
				addEdge(n.ID, pi.id, EdgeSelects)
			}
		}
		node := &out.Nodes[nodes[n.ID]]
		node.Detail = fmt.Sprintf("%d/%d", ready, total)
		// The service's color follows its pods' issues, which already carry
		// the grace periods; a not-ready pod mid-rollout does not paint it.
		switch {
		case total == 0:
			node.Status, node.Reason = TopologyWarning, "NoEndpoints"
		case ready == 0 && bad > 0:
			node.Status, node.Reason = TopologyError, "NoEndpoints"
		case bad > 0:
			node.Status, node.Reason = TopologyWarning, "EndpointsNotReady"
		}
	}

	for _, ing := range in.Ingresses {
		n := TopologyNode{ID: topoID("Ingress", ing.Namespace, ing.Name), Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name, Status: TopologyOK}
		var backends []string
		if b := ing.Spec.DefaultBackend; b != nil && b.Service != nil {
			backends = append(backends, b.Service.Name)
		}
		for _, rule := range ing.Spec.Rules {
			if n.Detail == "" && rule.Host != "" {
				n.Detail = rule.Host
			}
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil {
					backends = append(backends, path.Backend.Service.Name)
				}
			}
		}
		addNode(n)
		for _, name := range backends {
			target := topoID("Service", ing.Namespace, name)
			if !serviceIDs[target] {
				node := &out.Nodes[nodes[n.ID]]
				node.Status, node.Reason = TopologyWarning, "BackendNotFound"
				continue
			}
			addEdge(n.ID, target, EdgeRoutes)
		}
	}

	out.Total = len(out.Nodes)
	if out.Total > maxTopologyNodes {
		return Topology{Nodes: []TopologyNode{}, Edges: []TopologyEdge{}, Total: out.Total, TooLarge: true}
	}
	sort.SliceStable(out.Edges, func(i, j int) bool {
		if out.Edges[i].Source != out.Edges[j].Source {
			return out.Edges[i].Source < out.Edges[j].Source
		}
		return out.Edges[i].Target < out.Edges[j].Target
	})
	return out
}

func replicasOrOne(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

func applyIssue(n *TopologyNode, issue *Issue) {
	if issue == nil {
		return
	}
	n.Status = TopologyWarning
	if issue.Severity == SeverityError {
		n.Status = TopologyError
	}
	n.Reason = issue.Reason
}

func podIsReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// podOwnerID resolves the pod's workload node: through the ReplicaSet for
// Deployments, directly for StatefulSets and DaemonSets. Jobs are not in the
// graph (no required informer for them), so their pods stand alone.
func podOwnerID(p *corev1.Pod, rsOwner map[string]string) string {
	for _, ref := range p.OwnerReferences {
		switch ref.Kind {
		case "ReplicaSet":
			return rsOwner[p.Namespace+"/"+ref.Name]
		case "StatefulSet", "DaemonSet":
			return topoID(ref.Kind, p.Namespace, ref.Name)
		}
	}
	return ""
}

// templateConfig collects the ConfigMaps and Secrets a pod template mounts or
// reads env from, sorted and without duplicates.
func templateConfig(spec corev1.PodSpec) []string {
	seen := map[string]bool{}
	add := func(kind, name string) {
		if name != "" {
			seen[kind+"/"+name] = true
		}
	}
	for _, v := range spec.Volumes {
		if v.ConfigMap != nil {
			add("ConfigMap", v.ConfigMap.Name)
		}
		if v.Secret != nil {
			add("Secret", v.Secret.SecretName)
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.ConfigMap != nil {
					add("ConfigMap", src.ConfigMap.Name)
				}
				if src.Secret != nil {
					add("Secret", src.Secret.Name)
				}
			}
		}
	}
	for _, c := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
		for _, from := range c.EnvFrom {
			if from.ConfigMapRef != nil {
				add("ConfigMap", from.ConfigMapRef.Name)
			}
			if from.SecretRef != nil {
				add("Secret", from.SecretRef.Name)
			}
		}
		for _, env := range c.Env {
			if env.ValueFrom == nil {
				continue
			}
			if r := env.ValueFrom.ConfigMapKeyRef; r != nil {
				add("ConfigMap", r.Name)
			}
			if r := env.ValueFrom.SecretKeyRef; r != nil {
				add("Secret", r.Name)
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ClusterTopology builds the graph of the given namespaces (all if empty) from
// the informer cache. Ingresses and PVCs are optional informers: without them
// the graph is still served and Unavailable says why they are missing.
func (m *Manager) ClusterTopology(id int64, namespaces []string, now time.Time) (Topology, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return Topology{}, err
	}
	var in TopologyInput
	if in.Pods, err = listNamespaced(namespaces, func(ns string) ([]*corev1.Pod, error) {
		return conn.podLister.Pods(ns).List(labels.Everything())
	}); err != nil {
		return Topology{}, fmt.Errorf("k8s: failed to list pods for the topology: %w", err)
	}
	if in.Deployments, err = listNamespaced(namespaces, func(ns string) ([]*appsv1.Deployment, error) {
		return conn.deploymentLister.Deployments(ns).List(labels.Everything())
	}); err != nil {
		return Topology{}, fmt.Errorf("k8s: failed to list deployments for the topology: %w", err)
	}
	if in.StatefulSets, err = listNamespaced(namespaces, func(ns string) ([]*appsv1.StatefulSet, error) {
		return conn.statefulSetLister.StatefulSets(ns).List(labels.Everything())
	}); err != nil {
		return Topology{}, fmt.Errorf("k8s: failed to list statefulsets for the topology: %w", err)
	}
	if in.DaemonSets, err = listNamespaced(namespaces, func(ns string) ([]*appsv1.DaemonSet, error) {
		return conn.daemonSetLister.DaemonSets(ns).List(labels.Everything())
	}); err != nil {
		return Topology{}, fmt.Errorf("k8s: failed to list daemonsets for the topology: %w", err)
	}
	if in.ReplicaSets, err = listNamespaced(namespaces, func(ns string) ([]*appsv1.ReplicaSet, error) {
		return conn.replicaSetLister.ReplicaSets(ns).List(labels.Everything())
	}); err != nil {
		return Topology{}, fmt.Errorf("k8s: failed to list replicasets for the topology: %w", err)
	}
	if in.Services, err = listNamespaced(namespaces, func(ns string) ([]*corev1.Service, error) {
		return conn.serviceLister.Services(ns).List(labels.Everything())
	}); err != nil {
		return Topology{}, fmt.Errorf("k8s: failed to list services for the topology: %w", err)
	}

	unavailable := map[string]string{}
	optional := func(kind string) bool {
		_, err := m.getOptionalConn(id, kind)
		if errors.Is(err, ErrInformerNotSynced) {
			unavailable[kind] = err.Error()
			return false
		}
		return err == nil
	}
	if optional(KindIngresses) {
		if in.Ingresses, err = listNamespaced(namespaces, func(ns string) ([]*networkingv1.Ingress, error) {
			return conn.opt.ingresses.Ingresses(ns).List(labels.Everything())
		}); err != nil {
			return Topology{}, fmt.Errorf("k8s: failed to list ingresses for the topology: %w", err)
		}
	}
	if optional(KindPVCs) {
		if in.PVCs, err = listNamespaced(namespaces, func(ns string) ([]*corev1.PersistentVolumeClaim, error) {
			return conn.opt.pvcs.PersistentVolumeClaims(ns).List(labels.Everything())
		}); err != nil {
			return Topology{}, fmt.Errorf("k8s: failed to list persistentvolumeclaims for the topology: %w", err)
		}
	}

	out := BuildTopology(in, now)
	if len(unavailable) > 0 {
		out.Unavailable = unavailable
	}
	return out, nil
}
