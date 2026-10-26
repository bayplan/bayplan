package k8s

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Fixed now: the threshold logic depends on the clock, so the functions take it
// as a parameter and the tests need neither to wait nor to simulate time.
var now = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) metav1.Time { return metav1.NewTime(now.Add(-d)) }

func pod(name string, age time.Duration, mutate func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", CreationTimestamp: ago(age)},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if mutate != nil {
		mutate(p)
	}
	return p
}

func ready(n int, allReady bool) []corev1.ContainerStatus {
	out := make([]corev1.ContainerStatus, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, corev1.ContainerStatus{Name: "c", Ready: allReady})
	}
	return out
}

func TestPodIssue(t *testing.T) {
	tests := []struct {
		name         string
		pod          *corev1.Pod
		wantIssue    bool
		wantSeverity Severity
		wantReason   string
	}{
		{
			name: "running and ready is not a problem",
			pod: pod("ok", time.Hour, func(p *corev1.Pod) {
				p.Status.ContainerStatuses = ready(2, true)
			}),
		},
		{
			name: "succeeded is not a problem even if not ready",
			pod: pod("job", time.Hour, func(p *corev1.Pod) {
				p.Status.Phase = corev1.PodSucceeded
				p.Status.ContainerStatuses = ready(1, false)
			}),
			wantIssue: false,
		},
		{
			name: "crashloop is an error from the first moment",
			pod: pod("crash", 10*time.Second, func(p *corev1.Pod) {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{
					Name:  "app",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}}
			}),
			wantIssue: true, wantSeverity: SeverityError, wantReason: "CrashLoopBackOff",
		},
		{
			name: "freshly created ContainerCreating is not a problem",
			pod: pod("creating", 10*time.Second, func(p *corev1.Pod) {
				p.Status.Phase = corev1.PodPending
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{
					Name:  "app",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
				}}
			}),
		},
		{
			name: "pending past the grace is a warning",
			pod: pod("pend", 30*time.Minute, func(p *corev1.Pod) {
				p.Status.Phase = corev1.PodPending
			}),
			wantIssue: true, wantSeverity: SeverityWarning, wantReason: "Pending",
		},
		{
			name: "pending within the grace is not a problem",
			pod: pod("pend-nuevo", time.Minute, func(p *corev1.Pod) {
				p.Status.Phase = corev1.PodPending
			}),
		},
		{
			name: "not ready past the grace is a warning",
			pod: pod("notready", time.Hour, func(p *corev1.Pod) {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{
					{Name: "a", Ready: true},
					{Name: "b", Ready: false},
				}
			}),
			wantIssue: true, wantSeverity: SeverityWarning, wantReason: "NotReady",
		},
		{
			name: "failed is an error",
			pod: pod("failed", time.Hour, func(p *corev1.Pod) {
				p.Status.Phase = corev1.PodFailed
				p.Status.Reason = "Evicted"
			}),
			wantIssue: true, wantSeverity: SeverityError, wantReason: "Evicted",
		},
		{
			name: "recent terminating is not a problem",
			pod: pod("term", time.Hour, func(p *corev1.Pod) {
				ts := ago(10 * time.Second)
				p.DeletionTimestamp = &ts
			}),
		},
		{
			name: "stuck terminating is a warning",
			pod: pod("term-stuck", time.Hour, func(p *corev1.Pod) {
				ts := ago(30 * time.Minute)
				p.DeletionTimestamp = &ts
			}),
			wantIssue: true, wantSeverity: SeverityWarning, wantReason: "Terminating",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PodIssue(tt.pod, now)
			if !tt.wantIssue {
				if got != nil {
					t.Fatalf("expected nil, got %+v", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected an issue, got nil")
			}
			if got.Severity != tt.wantSeverity {
				t.Errorf("severity = %q, expected %q", got.Severity, tt.wantSeverity)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("reason = %q, expected %q", got.Reason, tt.wantReason)
			}
		})
	}
}

func deployment(name string, replicas, ready, available int32, mutate func(*appsv1.Deployment)) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", CreationTimestamp: ago(24 * time.Hour), Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: ready, AvailableReplicas: available, ObservedGeneration: 1,
			Conditions: []appsv1.DeploymentCondition{{LastUpdateTime: ago(24 * time.Hour)}},
		},
	}
	if mutate != nil {
		mutate(d)
	}
	return d
}

func TestDeploymentIssue(t *testing.T) {
	tests := []struct {
		name         string
		deployment   *appsv1.Deployment
		wantIssue    bool
		wantSeverity Severity
		wantReason   string
	}{
		{name: "complete is not a problem", deployment: deployment("ok", 3, 3, 3, nil)},
		{
			name:       "scaled to zero is not a problem",
			deployment: deployment("cero", 0, 0, 0, nil),
		},
		{
			name: "no available replica is an error without grace",
			deployment: deployment("caido", 3, 0, 0, func(d *appsv1.Deployment) {
				d.Status.Conditions = []appsv1.DeploymentCondition{{LastUpdateTime: ago(10 * time.Second)}}
			}),
			wantIssue: true, wantSeverity: SeverityError, wantReason: "Unavailable",
		},
		{
			name: "recent partial rollout is not a problem",
			deployment: deployment("rollout", 3, 2, 2, func(d *appsv1.Deployment) {
				d.Status.Conditions = []appsv1.DeploymentCondition{{LastUpdateTime: ago(time.Minute)}}
			}),
		},
		{
			name:       "partial rollout that is not progressing is a warning",
			deployment: deployment("rollout-viejo", 3, 2, 2, nil),
			wantIssue:  true, wantSeverity: SeverityWarning, wantReason: "RolloutIncomplete",
		},
		{
			name: "unobserved generation is a warning",
			deployment: deployment("stalled", 3, 3, 3, func(d *appsv1.Deployment) {
				d.Generation = 5
				d.Status.ObservedGeneration = 4
			}),
			wantIssue: true, wantSeverity: SeverityWarning, wantReason: "RolloutStalled",
		},
		{
			name:       "paused is a warning even when complete",
			deployment: deployment("paused", 3, 3, 3, func(d *appsv1.Deployment) { d.Spec.Paused = true }),
			wantIssue:  true, wantSeverity: SeverityWarning, wantReason: "Paused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeploymentIssue(tt.deployment, now)
			if !tt.wantIssue {
				if got != nil {
					t.Fatalf("expected nil, got %+v", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected an issue, got nil")
			}
			if got.Severity != tt.wantSeverity || got.Reason != tt.wantReason {
				t.Errorf("got %s/%s, expected %s/%s", got.Severity, got.Reason, tt.wantSeverity, tt.wantReason)
			}
		})
	}
}

func node(name string, conditions []corev1.NodeCondition, mutate func(*corev1.Node)) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: ago(30 * 24 * time.Hour)},
		Status:     corev1.NodeStatus{Conditions: conditions},
	}
	if mutate != nil {
		mutate(n)
	}
	return n
}

func cond(t corev1.NodeConditionType, s corev1.ConditionStatus, age time.Duration) corev1.NodeCondition {
	return corev1.NodeCondition{Type: t, Status: s, LastTransitionTime: ago(age)}
}

func TestNodeIssue(t *testing.T) {
	tests := []struct {
		name         string
		node         *corev1.Node
		wantIssue    bool
		wantSeverity Severity
		wantReason   string
	}{
		{
			name: "ready is not a problem",
			node: node("ok", []corev1.NodeCondition{cond(corev1.NodeReady, corev1.ConditionTrue, time.Hour)}, nil),
		},
		{
			name:      "notready is an error",
			node:      node("bad", []corev1.NodeCondition{cond(corev1.NodeReady, corev1.ConditionFalse, time.Hour)}, nil),
			wantIssue: true, wantSeverity: SeverityError, wantReason: "NotReady",
		},
		{
			name:      "unknown is a warning, not an error",
			node:      node("gone", []corev1.NodeCondition{cond(corev1.NodeReady, corev1.ConditionUnknown, time.Hour)}, nil),
			wantIssue: true, wantSeverity: SeverityWarning, wantReason: "Unknown",
		},
		{
			name: "recent notready does not fire (anti-flapping)",
			node: node("flap", []corev1.NodeCondition{cond(corev1.NodeReady, corev1.ConditionFalse, 10*time.Second)}, nil),
		},
		{
			name: "memory pressure is a warning",
			node: node("pressure", []corev1.NodeCondition{
				cond(corev1.NodeReady, corev1.ConditionTrue, time.Hour),
				cond(corev1.NodeMemoryPressure, corev1.ConditionTrue, time.Hour),
			}, nil),
			wantIssue: true, wantSeverity: SeverityWarning, wantReason: "MemoryPressure",
		},
		{
			name: "cordoned is a warning",
			node: node("cordon", []corev1.NodeCondition{cond(corev1.NodeReady, corev1.ConditionTrue, time.Hour)},
				func(n *corev1.Node) { n.Spec.Unschedulable = true }),
			wantIssue: true, wantSeverity: SeverityWarning, wantReason: "Cordoned",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NodeIssue(tt.node, now)
			if !tt.wantIssue {
				if got != nil {
					t.Fatalf("expected nil, got %+v", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected an issue, got nil")
			}
			if got.Severity != tt.wantSeverity || got.Reason != tt.wantReason {
				t.Errorf("got %s/%s, expected %s/%s", got.Severity, got.Reason, tt.wantSeverity, tt.wantReason)
			}
		})
	}
}

func TestSortIssuesPutsErrorsAndOldestFirst(t *testing.T) {
	issues := []Issue{
		{Severity: SeverityWarning, Kind: "Pod", Name: "w-viejo", Since: now.Add(-48 * time.Hour)},
		{Severity: SeverityError, Kind: "Pod", Name: "e-nuevo", Since: now.Add(-time.Minute)},
		{Severity: SeverityError, Kind: "Pod", Name: "e-viejo", Since: now.Add(-72 * time.Hour)},
		{Severity: SeverityWarning, Kind: "Pod", Name: "w-nuevo", Since: now.Add(-time.Minute)},
	}
	SortIssues(issues)

	want := []string{"e-viejo", "e-nuevo", "w-viejo", "w-nuevo"}
	for i, name := range want {
		if issues[i].Name != name {
			t.Fatalf("position %d = %q, expected %q (full order: %v)", i, issues[i].Name, name, names(issues))
		}
	}
}

func names(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Name)
	}
	return out
}

func TestClusterCapacityIgnoresTerminatedPodsAndUsesInitContainers(t *testing.T) {
	mustQty := func(s string) corev1.ResourceList {
		return corev1.ResourceList{corev1.ResourceCPU: mustParse(t, s)}
	}
	pods := []*corev1.Pod{
		// Counts: 100m + 200m.
		{
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
			Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Resources: corev1.ResourceRequirements{Requests: mustQty("100m")}},
				{Resources: corev1.ResourceRequirements{Requests: mustQty("200m")}},
			}},
		},
		// Does not count: it already finished and reserves nothing.
		{
			Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Resources: corev1.ResourceRequirements{Requests: mustQty("500m")}},
			}},
		},
		// The initContainer requests more than the sum of containers: the init wins.
		{
			Status: corev1.PodStatus{Phase: corev1.PodPending},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: mustQty("1")}}},
				Containers:     []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: mustQty("100m")}}},
			},
		},
	}
	nodes := []*corev1.Node{
		{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:  mustParse(t, "4"),
			corev1.ResourcePods: mustParse(t, "110"),
		}}},
	}

	got := clusterCapacity(pods, nodes)
	if got.PodsUsed != 2 {
		t.Errorf("PodsUsed = %d, expected 2 (the Succeeded one does not count)", got.PodsUsed)
	}
	if got.PodsAllocatable != 110 {
		t.Errorf("PodsAllocatable = %d, expected 110", got.PodsAllocatable)
	}
	// 300m from the first + 1000m from the third one's init.
	if got.CPURequestedMilli != 1300 {
		t.Errorf("CPURequestedMilli = %d, expected 1300", got.CPURequestedMilli)
	}
	if got.CPUAllocatableMilli != 4000 {
		t.Errorf("CPUAllocatableMilli = %d, expected 4000", got.CPUAllocatableMilli)
	}
}

func TestKubeletVersionsOnlyListsWhenMismatched(t *testing.T) {
	homogeneo := []*corev1.Node{
		{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.32.3"}}},
		{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.32.3"}}},
	}
	top, distinct := kubeletVersions(homogeneo)
	if top != "v1.32.3" || distinct != nil {
		t.Errorf("homogeneous: got (%q, %v), expected (v1.32.3, nil)", top, distinct)
	}

	mixto := []*corev1.Node{
		{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.32.3"}}},
		{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.32.3"}}},
		{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.31.0"}}},
	}
	top, distinct = kubeletVersions(mixto)
	if top != "v1.32.3" {
		t.Errorf("mixed: majority version = %q, expected v1.32.3", top)
	}
	if len(distinct) != 2 || distinct[0] != "v1.31.0" || distinct[1] != "v1.32.3" {
		t.Errorf("mixed: distinct = %v, expected [v1.31.0 v1.32.3] sorted", distinct)
	}
}

// mustParse avoids repeating resource.MustParse's error handling in every case;
// MustParse panics and a readable test failure is what matters here.
func mustParse(t *testing.T, s string) resource.Quantity {
	t.Helper()
	q, err := resource.ParseQuantity(s)
	if err != nil {
		t.Fatalf("invalid quantity %q: %v", s, err)
	}
	return q
}

func statefulSet(name string, replicas, ready int32, age time.Duration) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", CreationTimestamp: ago(age)},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: ready},
	}
}

func TestStatefulSetIssue(t *testing.T) {
	if got := StatefulSetIssue(statefulSet("ok", 3, 3, 24*time.Hour), now); got != nil {
		t.Errorf("complete: expected nil, got %+v", *got)
	}
	// They start one at a time and in order: a freshly created, half-done one is
	// NOT a problem, which is why everything goes through the rollout grace.
	if got := StatefulSetIssue(statefulSet("nuevo", 10, 2, time.Minute), now); got != nil {
		t.Errorf("freshly created: expected nil, got %+v", *got)
	}
	if got := StatefulSetIssue(statefulSet("parcial", 3, 2, 24*time.Hour), now); got == nil || got.Reason != "RolloutIncomplete" {
		t.Errorf("partial and old: expected RolloutIncomplete, got %+v", got)
	}
	if got := StatefulSetIssue(statefulSet("caido", 3, 0, 24*time.Hour), now); got == nil || got.Severity != SeverityError {
		t.Errorf("no ready replicas: expected error, got %+v", got)
	}
	if got := StatefulSetIssue(statefulSet("cero", 0, 0, 24*time.Hour), now); got != nil {
		t.Errorf("scaled to 0: expected nil, got %+v", *got)
	}
}

func daemonSet(name string, desired, ready, misscheduled int32, age time.Duration) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", CreationTimestamp: ago(age)},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: desired, NumberReady: ready, NumberMisscheduled: misscheduled,
		},
	}
}

func TestDaemonSetIssue(t *testing.T) {
	if got := DaemonSetIssue(daemonSet("ok", 5, 5, 0, 24*time.Hour), now); got != nil {
		t.Errorf("complete: expected nil, got %+v", *got)
	}
	// Without eligible nodes there is nothing to deploy.
	if got := DaemonSetIssue(daemonSet("sin-nodos", 0, 0, 0, 24*time.Hour), now); got != nil {
		t.Errorf("0 eligible nodes: expected nil, got %+v", *got)
	}
	if got := DaemonSetIssue(daemonSet("parcial", 5, 3, 0, 24*time.Hour), now); got == nil || got.Reason != "RolloutIncomplete" {
		t.Errorf("partial: expected RolloutIncomplete, got %+v", got)
	}
	if got := DaemonSetIssue(daemonSet("caido", 5, 0, 0, 24*time.Hour), now); got == nil || got.Severity != SeverityError {
		t.Errorf("no node ready: expected error, got %+v", got)
	}
	// Misscheduled does not wait for the grace: it is a configuration deviation,
	// not a rollout in progress.
	if got := DaemonSetIssue(daemonSet("descolocado", 5, 5, 2, time.Minute), now); got == nil || got.Reason != "Misscheduled" {
		t.Errorf("misscheduled: expected Misscheduled without grace, got %+v", got)
	}
}
