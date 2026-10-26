package k8s

import (
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func int32p(v int32) *int32 { return &v }

func ownedPod(name string, labels map[string]string, ownerKind, ownerName string, mutate func(*corev1.Pod)) *corev1.Pod {
	return pod(name, time.Hour, func(p *corev1.Pod) {
		p.Labels = labels
		p.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind, Name: ownerName}}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		p.Status.ContainerStatuses = ready(1, true)
		if mutate != nil {
			mutate(p)
		}
	})
}

func topologyFixture() TopologyInput {
	apiLabels := map[string]string{"app": "api"}
	crashing := func(p *corev1.Pod) {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
	}
	return TopologyInput{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns", CreationTimestamp: ago(time.Hour)},
			Spec: appsv1.DeploymentSpec{Replicas: int32p(3), Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: apiLabels},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{{Name: "cfg", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}}}}},
					Containers: []corev1.Container{{Name: "c", Env: []corev1.EnvVar{
						{Name: "DB", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-db"}, Key: "url"}}},
						{Name: "DB2", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-db"}, Key: "user"}}},
					}}},
				},
			}},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 2, AvailableReplicas: 2, Replicas: 3},
		}},
		ReplicaSets: []*appsv1.ReplicaSet{
			{ObjectMeta: metav1.ObjectMeta{Name: "api-6c8f5", Namespace: "ns", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api"}}}},
		},
		StatefulSets: []*appsv1.StatefulSet{{
			ObjectMeta: metav1.ObjectMeta{Name: "pg", Namespace: "ns", CreationTimestamp: ago(time.Hour)},
			Spec:       appsv1.StatefulSetSpec{Replicas: int32p(1), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "pg"}}}},
			Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
		}},
		Pods: []*corev1.Pod{
			ownedPod("api-6c8f5-a", apiLabels, "ReplicaSet", "api-6c8f5", nil),
			ownedPod("api-6c8f5-b", apiLabels, "ReplicaSet", "api-6c8f5", nil),
			ownedPod("api-6c8f5-c", apiLabels, "ReplicaSet", "api-6c8f5", crashing),
			ownedPod("pg-0", map[string]string{"app": "pg"}, "StatefulSet", "pg", func(p *corev1.Pod) {
				p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-pg-0"}}}}
			}),
			// A bare pod behind a service: the edge goes to the pod itself.
			pod("debug", time.Hour, func(p *corev1.Pod) { p.Labels = map[string]string{"app": "debug"} }),
			// Finished Job pod: left out.
			pod("migrate-xyz", time.Hour, func(p *corev1.Pod) {
				p.Labels = apiLabels
				p.Status.Phase = corev1.PodSucceeded
			}),
		},
		Services: []*corev1.Service{
			{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"}, Spec: corev1.ServiceSpec{Selector: apiLabels}},
			{ObjectMeta: metav1.ObjectMeta{Name: "pg", Namespace: "ns"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "pg"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: "ns"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "debug"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "ns"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "gone"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: "ns"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName}},
			// Same labels in another namespace must not match.
			{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "other"}, Spec: corev1.ServiceSpec{Selector: apiLabels}},
		},
		Ingresses: []*networkingv1.Ingress{{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
			Spec: networkingv1.IngressSpec{
				DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "debug"}},
				Rules: []networkingv1.IngressRule{{Host: "shop.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
					{Path: "/api", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api"}}},
					{Path: "/old", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "missing"}}},
				}}}}},
			},
		}},
		PVCs: []*corev1.PersistentVolumeClaim{
			{ObjectMeta: metav1.ObjectMeta{Name: "data-pg-0", Namespace: "ns", CreationTimestamp: ago(time.Hour)}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}},
			{ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "ns", CreationTimestamp: ago(time.Minute)}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}},
		},
	}
}

func findNode(t *testing.T, topo Topology, id string) TopologyNode {
	t.Helper()
	for _, n := range topo.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %s not in the topology", id)
	return TopologyNode{}
}

func TestBuildTopologyEdges(t *testing.T) {
	topo := BuildTopology(topologyFixture(), now)
	want := []TopologyEdge{
		{"Deployment/ns/api", "Pod/ns/api-6c8f5-a", EdgeOwns},
		{"Deployment/ns/api", "Pod/ns/api-6c8f5-b", EdgeOwns},
		{"Deployment/ns/api", "Pod/ns/api-6c8f5-c", EdgeOwns},
		{"Ingress/ns/web", "Service/ns/api", EdgeRoutes},
		{"Ingress/ns/web", "Service/ns/debug", EdgeRoutes},
		{"Pod/ns/pg-0", "PersistentVolumeClaim/ns/data-pg-0", EdgeMounts},
		{"Service/ns/api", "Deployment/ns/api", EdgeSelects},
		{"Service/ns/debug", "Pod/ns/debug", EdgeSelects},
		{"Service/ns/pg", "StatefulSet/ns/pg", EdgeSelects},
		{"StatefulSet/ns/pg", "Pod/ns/pg-0", EdgeOwns},
	}
	if !slices.Equal(topo.Edges, want) {
		t.Fatalf("edges:\n got %v\nwant %v", topo.Edges, want)
	}
	for _, n := range topo.Nodes {
		if n.Name == "migrate-xyz" {
			t.Fatal("a Succeeded pod must not be in the graph")
		}
		if n.Kind == "ReplicaSet" {
			t.Fatal("a Deployment's ReplicaSet must be folded into it")
		}
	}
}

func TestBuildTopologyStatus(t *testing.T) {
	topo := BuildTopology(topologyFixture(), now)
	tests := []struct {
		id, status, reason, detail string
	}{
		{"Pod/ns/api-6c8f5-c", TopologyError, "CrashLoopBackOff", ""},
		{"Pod/ns/api-6c8f5-a", TopologyOK, "", "Running"},
		{"Service/ns/api", TopologyWarning, "EndpointsNotReady", "2/3"},
		{"Service/ns/orphan", TopologyWarning, "NoEndpoints", "0/0"},
		{"Service/ns/external", TopologyOK, "", ""},
		{"Ingress/ns/web", TopologyWarning, "BackendNotFound", "shop.example.com"},
		// Same verdict as the overview: 2/3 for longer than the rollout grace.
		{"Deployment/ns/api", TopologyWarning, "RolloutIncomplete", "2/3"},
		// Pending for a minute is a claim waiting for its consumer, not a problem.
		{"PersistentVolumeClaim/ns/fresh", TopologyOK, "", ""},
	}
	for _, tt := range tests {
		n := findNode(t, topo, tt.id)
		if n.Status != tt.status || n.Reason != tt.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", tt.id, n.Status, n.Reason, tt.status, tt.reason)
		}
		if tt.detail != "" && n.Detail != tt.detail {
			t.Errorf("%s: detail %q, want %q", tt.id, n.Detail, tt.detail)
		}
	}
	if got := findNode(t, topo, "Pod/ns/api-6c8f5-a").Owner; got != "Deployment/ns/api" {
		t.Errorf("pod owner %q, want the Deployment", got)
	}
	if got := findNode(t, topo, "Deployment/ns/api").Config; !slices.Equal(got, []string{"ConfigMap/api-config", "Secret/api-db"}) {
		t.Errorf("config refs %v", got)
	}
}

func TestBuildTopologyBareReplicaSet(t *testing.T) {
	in := TopologyInput{
		ReplicaSets: []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "ns"}, Spec: appsv1.ReplicaSetSpec{Replicas: int32p(1)}}},
		Pods:        []*corev1.Pod{ownedPod("manual-x", nil, "ReplicaSet", "manual", nil)},
	}
	topo := BuildTopology(in, now)
	if got := findNode(t, topo, "Pod/ns/manual-x").Owner; got != "ReplicaSet/ns/manual" {
		t.Fatalf("a bare ReplicaSet must own its pods, got %q", got)
	}
}

func TestBuildTopologyTooLarge(t *testing.T) {
	var in TopologyInput
	for i := 0; i <= maxTopologyNodes; i++ {
		in.Pods = append(in.Pods, pod("p"+time.Duration(i).String(), time.Hour, nil))
	}
	topo := BuildTopology(in, now)
	if !topo.TooLarge || len(topo.Nodes) != 0 || topo.Total != maxTopologyNodes+1 {
		t.Fatalf("got tooLarge=%v nodes=%d total=%d", topo.TooLarge, len(topo.Nodes), topo.Total)
	}
}
