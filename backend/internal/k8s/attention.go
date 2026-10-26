package k8s

import (
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// Persistence thresholds before something is considered to "need attention".
// Without them, any normal deployment (a pod starting up, a rollout in
// progress) would show up as a problem for a few seconds and the panel would
// always be red, which is the fastest way to make nobody believe it. It is the
// usual monitoring criterion: fire only when the condition persists for a
// minimum duration.
const (
	// Pods starting up or not yet ready: image pulls, init containers and
	// readiness probes need some leeway.
	podGrace = 5 * time.Minute
	// A rollout that is not progressing. Longer than the pod one: a large
	// deployment takes a while to cycle through all its replicas.
	rolloutGrace = 10 * time.Minute
	// Node conditions. Deliberately short: a NotReady node is serious and should
	// not be hidden, but a few seconds of flapping are filtered out.
	nodeGrace = 2 * time.Minute
	// A pod in Terminating for longer than this is stuck (a finalizer that never
	// finishes, a kubelet that does not respond), not in a normal deletion.
	terminatingGrace = 5 * time.Minute
)

// Severity of a problem. Deliberately only two levels: "something is broken"
// and "something is not as it should be". A third, intermediate level forces a
// decision in every case and does not change what the reader does.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is a concrete, actionable problem, not a metric. The UI renders them as
// a prioritized list ("Needs attention"), not as a chart.
type Issue struct {
	Severity  Severity `json:"severity"`
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name"`
	// Reason is a stable code (CrashLoopBackOff, RolloutIncomplete...): the UI
	// translates it and uses it to group. Never human-readable text.
	Reason string `json:"reason"`
	// Detail carries the numbers that give context ("0/3 replicas ready"),
	// already formatted because there is nothing to translate in them.
	Detail string `json:"detail,omitempty"`
	// Since is when it was first observed; the UI shows the age.
	Since time.Time `json:"since"`
}

// Container waiting reasons that are a real failure and not a normal stage of
// startup. Anything not listed here that is not a healthy state either is
// treated as a warning: the list of Kubernetes reasons grows with every version
// and silencing an unknown reason is worse than over-warning.
var containerErrorReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
	"CNIConfigError":             true,
}

// PodIssue returns the pod's problem, or nil if it is fine. Pure function: it
// receives the object exactly as it sits in the informer cache plus the current
// time.
func PodIssue(pod *corev1.Pod, now time.Time) *Issue {
	base := Issue{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, Since: pod.CreationTimestamp.Time}

	if pod.DeletionTimestamp != nil {
		// A normal deletion takes seconds; if it is still here, it is stuck.
		if now.Sub(pod.DeletionTimestamp.Time) < terminatingGrace {
			return nil
		}
		base.Severity = SeverityWarning
		base.Reason = "Terminating"
		base.Since = pod.DeletionTimestamp.Time
		return &base
	}

	// A pod that finished successfully (a Job, a CronJob) is not a problem.
	if pod.Status.Phase == corev1.PodSucceeded {
		return nil
	}

	if pod.Status.Phase == corev1.PodFailed {
		base.Severity = SeverityError
		base.Reason = "Failed"
		if pod.Status.Reason != "" {
			base.Reason = pod.Status.Reason
		}
		return &base
	}

	// The specific reason outweighs the phase: a pod in phase Running with a
	// container in CrashLoopBackOff is broken, not running.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting == nil || cs.State.Waiting.Reason == "" {
			continue
		}
		reason := cs.State.Waiting.Reason
		if !containerErrorReasons[reason] {
			continue
		}
		base.Severity = SeverityError
		base.Reason = reason
		base.Detail = fmt.Sprintf("%s · %d restarts", cs.Name, cs.RestartCount)
		if at := lastTransition(pod, cs.Name); !at.IsZero() {
			base.Since = at
		}
		return &base
	}

	// Whatever is still unresolved only counts once the startup grace is over.
	age := now.Sub(pod.CreationTimestamp.Time)
	if age < podGrace {
		return nil
	}

	if pod.Status.Phase == corev1.PodPending {
		base.Severity = SeverityWarning
		base.Reason = "Pending"
		base.Detail = podScheduleDetail(pod)
		return &base
	}

	ready, total := podReadyCounts(pod)
	if total > 0 && ready < total {
		base.Severity = SeverityWarning
		base.Reason = "NotReady"
		base.Detail = fmt.Sprintf("%d/%d containers ready", ready, total)
		return &base
	}

	return nil
}

// DeploymentIssue returns the deployment's problem, or nil if it is fine.
func DeploymentIssue(d *appsv1.Deployment, now time.Time) *Issue {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	base := Issue{
		Kind: "Deployment", Namespace: d.Namespace, Name: d.Name,
		Since: d.CreationTimestamp.Time,
	}
	// Manually scaling to 0 is a decision, not a failure.
	if want == 0 {
		if d.Spec.Paused {
			base.Severity = SeverityWarning
			base.Reason = "Paused"
			return &base
		}
		return nil
	}

	if newer := latestConditionTime(d); !newer.IsZero() {
		base.Since = newer
	}

	// The controller has not yet seen the latest version of the spec: if this
	// persists, the rollout is genuinely stuck (and not just "in progress").
	if d.Status.ObservedGeneration < d.Generation && now.Sub(base.Since) >= rolloutGrace {
		base.Severity = SeverityWarning
		base.Reason = "RolloutStalled"
		base.Detail = fmt.Sprintf("generation %d observed, spec at %d", d.Status.ObservedGeneration, d.Generation)
		return &base
	}

	if d.Spec.Paused {
		base.Severity = SeverityWarning
		base.Reason = "Paused"
		return &base
	}

	// No available replica is a service that is down, not a slow rollout: it
	// counts as an error from the very first moment, with no grace.
	if d.Status.AvailableReplicas == 0 {
		base.Severity = SeverityError
		base.Reason = "Unavailable"
		base.Detail = fmt.Sprintf("0/%d replicas available", want)
		return &base
	}

	if d.Status.ReadyReplicas < want && now.Sub(base.Since) >= rolloutGrace {
		base.Severity = SeverityWarning
		base.Reason = "RolloutIncomplete"
		base.Detail = fmt.Sprintf("%d/%d replicas ready", d.Status.ReadyReplicas, want)
		return &base
	}

	return nil
}

// StatefulSetIssue applies the same criterion as DeploymentIssue, with one
// important difference: StatefulSets start their pods ONE AT A TIME and in
// order, so one with 10 replicas legitimately takes much longer to be complete.
// That is why there is no "0 available = immediate error" case here: everything
// goes through the rollout grace.
func StatefulSetIssue(s *appsv1.StatefulSet, now time.Time) *Issue {
	want := int32(1)
	if s.Spec.Replicas != nil {
		want = *s.Spec.Replicas
	}
	if want == 0 {
		return nil
	}

	base := Issue{
		Kind: "StatefulSet", Namespace: s.Namespace, Name: s.Name,
		Since: s.CreationTimestamp.Time,
	}
	if s.Status.ReadyReplicas >= want || now.Sub(base.Since) < rolloutGrace {
		return nil
	}

	base.Detail = fmt.Sprintf("%d/%d replicas ready", s.Status.ReadyReplicas, want)
	if s.Status.ReadyReplicas == 0 {
		base.Severity = SeverityError
		base.Reason = "Unavailable"
		return &base
	}
	base.Severity = SeverityWarning
	base.Reason = "RolloutIncomplete"
	return &base
}

// DaemonSetIssue uses the resource's own vocabulary: there are no desired
// replicas, there is one pod per eligible node.
func DaemonSetIssue(d *appsv1.DaemonSet, now time.Time) *Issue {
	base := Issue{
		Kind: "DaemonSet", Namespace: d.Namespace, Name: d.Name,
		Since: d.CreationTimestamp.Time,
	}

	// Misscheduled are pods running on nodes where they no longer should (a
	// selector or a taint changed): it does not break the service but it is a
	// deviation worth seeing, and it does not depend on the rollout grace.
	if d.Status.NumberMisscheduled > 0 {
		base.Severity = SeverityWarning
		base.Reason = "Misscheduled"
		base.Detail = fmt.Sprintf("%d pods on nodes where they do not belong", d.Status.NumberMisscheduled)
		return &base
	}

	// Without eligible nodes there is nothing to deploy: not a failure.
	if d.Status.DesiredNumberScheduled == 0 {
		return nil
	}
	if d.Status.NumberReady >= d.Status.DesiredNumberScheduled || now.Sub(base.Since) < rolloutGrace {
		return nil
	}

	base.Detail = fmt.Sprintf("%d/%d nodes ready", d.Status.NumberReady, d.Status.DesiredNumberScheduled)
	if d.Status.NumberReady == 0 {
		base.Severity = SeverityError
		base.Reason = "Unavailable"
		return &base
	}
	base.Severity = SeverityWarning
	base.Reason = "RolloutIncomplete"
	return &base
}

// NodeIssue returns the node's problem, or nil if it is fine.
func NodeIssue(n *corev1.Node, now time.Time) *Issue {
	base := Issue{Kind: "Node", Name: n.Name, Since: n.CreationTimestamp.Time}

	for _, cond := range n.Status.Conditions {
		if cond.Type != corev1.NodeReady {
			continue
		}
		if cond.Status == corev1.ConditionTrue {
			break
		}
		if now.Sub(cond.LastTransitionTime.Time) < nodeGrace {
			return nil
		}
		base.Since = cond.LastTransitionTime.Time
		base.Detail = cond.Message
		// Unknown is a node that stopped reporting: in practice it is usually a
		// spot interruption or a scale-down, not a failure. NotReady is a node
		// that responds and says it is not fine: that one is an error.
		if cond.Status == corev1.ConditionUnknown {
			base.Severity = SeverityWarning
			base.Reason = "Unknown"
		} else {
			base.Severity = SeverityError
			base.Reason = "NotReady"
		}
		return &base
	}

	// Resource pressure: the node works but is at its limit and the scheduler
	// is already avoiding it or evicting pods.
	for _, cond := range n.Status.Conditions {
		switch cond.Type {
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable:
			if cond.Status != corev1.ConditionTrue || now.Sub(cond.LastTransitionTime.Time) < nodeGrace {
				continue
			}
			base.Severity = SeverityWarning
			base.Reason = string(cond.Type)
			base.Detail = cond.Message
			base.Since = cond.LastTransitionTime.Time
			return &base
		}
	}

	if n.Spec.Unschedulable {
		base.Severity = SeverityWarning
		base.Reason = "Cordoned"
		return &base
	}

	return nil
}

// SortIssues orders problems the way they are read: errors first, and within
// each severity the oldest at the top (a failure that has been pending for
// three days matters more than one from a minute ago).
func SortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Severity != b.Severity {
			return a.Severity == SeverityError
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since)
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
}

func podReadyCounts(pod *corev1.Pod) (ready, total int) {
	for _, cs := range pod.Status.ContainerStatuses {
		total++
		if cs.Ready {
			ready++
		}
	}
	return ready, total
}

// podScheduleDetail returns why a pod is still Pending when the scheduler has
// written it down (typically FailedScheduling with "insufficient cpu" or
// similar). It is the information that turns a useless "Pending" into
// something actionable.
func podScheduleDetail(pod *corev1.Pod) string {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status != corev1.ConditionTrue {
			if cond.Message != "" {
				return cond.Message
			}
			return cond.Reason
		}
	}
	if pod.Spec.NodeName == "" {
		return "no node assigned"
	}
	return ""
}

// lastTransition finds when a container's current state began. The waiting
// state carries no timestamp, so the one from the last terminated state (the
// previous failed start) is used, which is the closest thing to "broken since
// when".
func lastTransition(pod *corev1.Pod, container string) time.Time {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != container {
			continue
		}
		if cs.LastTerminationState.Terminated != nil {
			return cs.LastTerminationState.Terminated.FinishedAt.Time
		}
	}
	return time.Time{}
}

// latestConditionTime is the last time the deployment controller touched any of
// its conditions: a better reference for "since when is this happening" than
// the creation date, which on an old deployment would give absurd ages.
func latestConditionTime(d *appsv1.Deployment) time.Time {
	var latest time.Time
	for _, cond := range d.Status.Conditions {
		if cond.LastUpdateTime.Time.After(latest) {
			latest = cond.LastUpdateTime.Time
		}
	}
	return latest
}
