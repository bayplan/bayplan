package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"
)

// fieldManager identifies the panel in the managedFields of the objects it
// applies, like kubectl does with "kubectl-client-side-apply".
const fieldManager = "bayplan"

// maxApplyDocs caps how many documents a single apply accepts: a YAML pasted
// by mistake with a whole cluster dump must not turn into 400 requests to the
// API server.
const maxApplyDocs = 50

// ErrInvalidManifest wraps the parse/validation errors of a YAML pasted by the
// user (400, not 500).
var ErrInvalidManifest = errors.New("invalid manifest")

// ApplyResult describes what happened to each document of the YAML.
type ApplyResult struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	// Operation: "created" if it did not exist, "configured" if it did. With
	// dryRun it says what WOULD have happened.
	Operation string `json:"operation"`
}

// restMapperFor builds a RESTMapper from the cluster's discovery, with the
// impersonated identity (discovery is allowed to every authenticated user via
// system:discovery). It is created per request: apply is a one-off admin action
// and caching it would force invalidating it when new CRDs are installed.
func (c *clusterConn) restMapperFor(actingUser, actingGroup string) (meta.RESTMapper, dynamic.Interface, error) {
	cfg := c.impersonatedRestConfig(actingUser, actingGroup)
	// Discovery takes no context: bounded per request (it never streams).
	disc, err := discovery.NewDiscoveryClientForConfig(boundedConfig(cfg))
	if err != nil {
		return nil, nil, fmt.Errorf("k8s: failed to create discovery client: %w", err)
	}
	// The RESTMapper swallows discovery errors and returns "unknown" for any
	// kind, which hides the real cause (almost always that the cluster does not
	// allow impersonating this group because the RBAC has not been re-applied).
	// A cheap call to /version brings it to light.
	if _, err := disc.ServerVersion(); err != nil {
		return nil, nil, fmt.Errorf("k8s: the cluster rejects identity %s (%s): %w", actingUser, actingGroup, err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc))
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("k8s: failed to create dynamic client: %w", err)
	}
	return mapper, dyn, nil
}

// ParseManifests decodes a YAML (one or several documents separated by ---)
// into objects. It validates the minimum needed to be able to apply them:
// apiVersion, kind and metadata.name.
func ParseManifests(raw string) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(raw)), 4096)
	var out []*unstructured.Unstructured
	for {
		var obj map[string]any
		err := dec.Decode(&obj)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
		}
		if len(obj) == 0 {
			continue // empty document (e.g. a trailing "---")
		}
		u := &unstructured.Unstructured{Object: obj}
		if u.GetAPIVersion() == "" || u.GetKind() == "" {
			return nil, fmt.Errorf("%w: every document needs apiVersion and kind", ErrInvalidManifest)
		}
		if u.GetName() == "" && u.GetGenerateName() == "" {
			return nil, fmt.Errorf("%w: %s without metadata.name", ErrInvalidManifest, u.GetKind())
		}
		out = append(out, u)
		if len(out) > maxApplyDocs {
			return nil, fmt.Errorf("%w: too many documents (max. %d)", ErrInvalidManifest, maxApplyDocs)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the YAML contains no objects", ErrInvalidManifest)
	}
	return out, nil
}

// Apply applies the documents with Server-Side Apply (force=true, like
// `kubectl apply --server-side --force-conflicts`): it takes ownership of the
// fields even if another field manager managed them, which is what someone
// editing a YAML by hand in a panel wants. With dryRun the API server validates
// and computes the result without persisting anything, and the panel uses it as
// a mandatory step before applying for real.
//
// Objects without a namespace in a namespaced kind go to defaultNamespace (what
// the user has selected), just like kubectl does with the context's namespace.
func (m *Manager) Apply(ctx context.Context, id int64, objs []*unstructured.Unstructured, defaultNamespace, actingUser, actingGroup string, dryRun bool) ([]ApplyResult, error) {
	conn, err := m.getConn(id)
	if err != nil {
		return nil, err
	}
	mapper, dyn, err := conn.restMapperFor(actingUser, actingGroup)
	if err != nil {
		return nil, err
	}

	// Resolve every mapping and run the protected-namespace guard BEFORE
	// applying anything, so a rejected document does not leave the previous
	// ones half-applied. The guard needs the scope: a cluster-scoped object
	// (a ClusterRole) has no namespace, so defaultNamespace being the
	// protected one says nothing about it; and the Namespace object itself
	// is guarded by its name (applying it could relabel or take over the
	// panel's namespace).
	mappings := make([]*meta.RESTMapping, len(objs))
	for i, obj := range objs {
		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return nil, fmt.Errorf("%w: the cluster does not know %s/%s: %v", ErrInvalidManifest, gvk.GroupVersion(), gvk.Kind, err)
		}
		mappings[i] = mapping
		ns := obj.GetNamespace()
		if ns == "" {
			ns = defaultNamespace
		}
		if target := guardTarget(mapping.Scope.Name() == meta.RESTScopeNameNamespace, gvk.GroupKind(), ns, obj.GetName()); target != "" {
			if err := m.GuardNamespace(id, target); err != nil {
				return nil, err
			}
		}
		if err := GuardPanelIdentity(obj); err != nil {
			return nil, err
		}
	}
	opts := metav1.PatchOptions{FieldManager: fieldManager, Force: ptr(true)}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}

	results := make([]ApplyResult, 0, len(objs))
	for i, obj := range objs {
		gvk := obj.GroupVersionKind()
		mapping := mappings[i]
		var res dynamic.ResourceInterface
		ns := obj.GetNamespace()
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			if ns == "" {
				ns = defaultNamespace
				if ns == "" {
					ns = "default"
				}
				obj.SetNamespace(ns)
			}
			res = dyn.Resource(mapping.Resource).Namespace(ns)
		} else {
			ns = ""
			obj.SetNamespace("")
			res = dyn.Resource(mapping.Resource)
		}

		// To tell "created" from "configured" we ask beforehand; with SSA the API
		// server does not say. A 404 here is not an error.
		operation := "configured"
		if _, err := res.Get(ctx, obj.GetName(), metav1.GetOptions{}); apierrors.IsNotFound(err) {
			operation = "created"
		}

		data, err := json.Marshal(obj.Object)
		if err != nil {
			return results, fmt.Errorf("k8s: failed to serialize %s/%s: %w", gvk.Kind, obj.GetName(), err)
		}
		if _, err := res.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, opts); err != nil {
			return results, fmt.Errorf("%s/%s: %w", gvk.Kind, obj.GetName(), err)
		}
		results = append(results, ApplyResult{Kind: gvk.Kind, APIVersion: gvk.GroupVersion().String(), Namespace: ns, Name: obj.GetName(), Operation: operation})
	}
	return results, nil
}

func ptr[T any](v T) *T { return &v }

// guardTarget returns the namespace a write to this object touches for the
// protected-namespace guard: its namespace if the kind is namespaced, its NAME
// if it is the Namespace object itself, and "" for any other cluster-scoped
// kind (a ClusterRole lives in no namespace). Pure (write_test.go).
func guardTarget(namespaced bool, gk schema.GroupKind, namespace, name string) string {
	switch {
	case namespaced:
		return namespace
	case gk.Group == "" && gk.Kind == "Namespace":
		return name
	default:
		return ""
	}
}

// DeleteResource deletes an object of any of the kinds the panel knows
// (yamlKinds), with background propagation (like kubectl delete by default).
// The RESTMapper resolves the plural to the cluster's real GVR.
func (m *Manager) DeleteResource(ctx context.Context, id int64, kind, namespace, name, actingUser, actingGroup string) error {
	k, ok := yamlKinds[kind]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	// For a namespaced kind the guard is on its namespace; for the Namespace
	// object itself it is on its name (namespace is "" for cluster-scoped
	// kinds, so guarding only that let `DELETE namespaces/-/<protected>`
	// through, which takes the panel and its database with it).
	if guarded := guardTarget(k.namespaced, schema.FromAPIVersionAndKind(k.apiVersion, k.kind).GroupKind(), namespace, name); guarded != "" {
		if err := m.GuardNamespace(id, guarded); err != nil {
			return err
		}
	}
	conn, err := m.getConn(id)
	if err != nil {
		return err
	}
	mapper, dyn, err := conn.restMapperFor(actingUser, actingGroup)
	if err != nil {
		return err
	}
	gvk := schema.FromAPIVersionAndKind(k.apiVersion, k.kind)
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("k8s: the cluster does not know %s: %w", gvk, err)
	}
	var res dynamic.ResourceInterface = dyn.Resource(mapping.Resource)
	if k.namespaced {
		res = dyn.Resource(mapping.Resource).Namespace(namespace)
	}
	policy := metav1.DeletePropagationBackground
	if err := res.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy}); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrResourceNotFound
		}
		return fmt.Errorf("k8s: failed to delete %s %s/%s: %w", k.kind, namespace, name, err)
	}
	return nil
}

// RestartWorkload does a rollout restart of a Deployment, StatefulSet or
// DaemonSet (same annotation as kubectl in all three).
func (m *Manager) RestartWorkload(ctx context.Context, id int64, kind, namespace, name, actingUser, actingGroup string) error {
	if err := m.GuardNamespace(id, namespace); err != nil {
		return err
	}
	conn, err := m.getConn(id)
	if err != nil {
		return err
	}
	client, err := conn.actionClientset(actingUser, actingGroup)
	if err != nil {
		return fmt.Errorf("k8s: failed to prepare action client: %w", err)
	}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"annotations": map[string]any{
		"kubectl.kubernetes.io/restartedAt": time.Now().Format(time.RFC3339),
	}}}}})
	switch kind {
	case "deployments":
		_, err = client.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case "statefulsets":
		_, err = client.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case "daemonsets":
		_, err = client.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	default:
		return fmt.Errorf("%w: %q does not support restart", ErrUnsupportedKind, kind)
	}
	if err != nil {
		return fmt.Errorf("k8s: failed to restart %s %s/%s: %w", kind, namespace, name, err)
	}
	return nil
}

// ScaleWorkload changes the replicas of a Deployment or StatefulSet via the
// scale subresource, with retry on resourceVersion conflict.
func (m *Manager) ScaleWorkload(ctx context.Context, id int64, kind, namespace, name string, replicas int32, actingUser, actingGroup string) error {
	if err := m.GuardNamespace(id, namespace); err != nil {
		return err
	}
	conn, err := m.getConn(id)
	if err != nil {
		return err
	}
	client, err := conn.actionClientset(actingUser, actingGroup)
	if err != nil {
		return fmt.Errorf("k8s: failed to prepare action client: %w", err)
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		switch kind {
		case "deployments":
			scale, err := client.AppsV1().Deployments(namespace).GetScale(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			scale.Spec.Replicas = replicas
			_, err = client.AppsV1().Deployments(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
			return err
		case "statefulsets":
			scale, err := client.AppsV1().StatefulSets(namespace).GetScale(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			scale.Spec.Replicas = replicas
			_, err = client.AppsV1().StatefulSets(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
			return err
		}
		return fmt.Errorf("%w: %q does not support scale", ErrUnsupportedKind, kind)
	})
	if err != nil {
		return fmt.Errorf("k8s: failed to scale %s %s/%s: %w", kind, namespace, name, err)
	}
	return nil
}

// RollbackDeployment goes back to the previous revision, like `kubectl rollout
// undo`: it finds the ReplicaSet of the revision immediately before the current
// one and copies its pod template into the deployment. Returns the restored
// revision.
func (m *Manager) RollbackDeployment(ctx context.Context, id int64, namespace, name, actingUser, actingGroup string) (int64, error) {
	if err := m.GuardNamespace(id, namespace); err != nil {
		return 0, err
	}
	conn, err := m.getConn(id)
	if err != nil {
		return 0, err
	}
	client, err := conn.actionClientset(actingUser, actingGroup)
	if err != nil {
		return 0, fmt.Errorf("k8s: failed to prepare action client: %w", err)
	}
	d, err := client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return 0, ErrResourceNotFound
		}
		return 0, fmt.Errorf("k8s: failed to read deployment: %w", err)
	}
	current, _ := strconv.ParseInt(d.Annotations["deployment.kubernetes.io/revision"], 10, 64)

	rsList, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: metav1.FormatLabelSelector(d.Spec.Selector)})
	if err != nil {
		return 0, fmt.Errorf("k8s: failed to list replicasets: %w", err)
	}
	var candidates []appsv1.ReplicaSet
	for _, rs := range rsList.Items {
		if !metav1.IsControlledBy(&rs, d) {
			continue
		}
		rev, err := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
		if err != nil || rev >= current {
			continue
		}
		candidates = append(candidates, rs)
	}
	if len(candidates) == 0 {
		return 0, fmt.Errorf("%w: there is no previous revision to go back to", ErrInvalidManifest)
	}
	sort.Slice(candidates, func(i, j int) bool {
		ri, _ := strconv.ParseInt(candidates[i].Annotations["deployment.kubernetes.io/revision"], 10, 64)
		rj, _ := strconv.ParseInt(candidates[j].Annotations["deployment.kubernetes.io/revision"], 10, 64)
		return ri > rj
	})
	target := candidates[0]
	targetRev, _ := strconv.ParseInt(target.Annotations["deployment.kubernetes.io/revision"], 10, 64)

	// The RS template carries the pod-template-hash label the controller adds;
	// it must not be on the deployment.
	tpl := target.Spec.Template.DeepCopy()
	delete(tpl.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	patch, err := json.Marshal([]map[string]any{{"op": "replace", "path": "/spec/template", "value": tpl}})
	if err != nil {
		return 0, fmt.Errorf("k8s: failed to serialize patch: %w", err)
	}
	if _, err := client.AppsV1().Deployments(namespace).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{}); err != nil {
		return 0, fmt.Errorf("k8s: failed to roll back %s/%s: %w", namespace, name, err)
	}
	return targetRev, nil
}

// CordonNode marks (or unmarks) a node as unschedulable.
func (m *Manager) CordonNode(ctx context.Context, id int64, name string, cordon bool, actingUser, actingGroup string) error {
	conn, err := m.getConn(id)
	if err != nil {
		return err
	}
	client, err := conn.actionClientset(actingUser, actingGroup)
	if err != nil {
		return fmt.Errorf("k8s: failed to prepare action client: %w", err)
	}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"unschedulable": cordon}})
	if _, err := client.CoreV1().Nodes().Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrResourceNotFound
		}
		return fmt.Errorf("k8s: failed to change cordon of %s: %w", name, err)
	}
	return nil
}

// DrainResult summarizes a drain: how many pods were sent for eviction and
// which could not be (PodDisruptionBudget, errors).
type DrainResult struct {
	Evicted []string `json:"evicted"`
	Skipped []string `json:"skipped"` // DaemonSets and static (mirror) pods, like kubectl drain --ignore-daemonsets
	Failed  []string `json:"failed"`  // "ns/pod: reason"
}

// DrainNode cordons and requests the eviction (Eviction API, honors
// PodDisruptionBudgets) of every pod on the node except DaemonSet and static
// ones. It does not wait for them to finish: the Eviction API responds when it
// accepts the request, and waiting here would tie an HTTP request to something
// that can take minutes. The UI sees the progress in the pod list.
func (m *Manager) DrainNode(ctx context.Context, id int64, name, actingUser, actingGroup string) (DrainResult, error) {
	conn, err := m.getConn(id)
	if err != nil {
		return DrainResult{}, err
	}
	client, err := conn.actionClientset(actingUser, actingGroup)
	if err != nil {
		return DrainResult{}, fmt.Errorf("k8s: failed to prepare action client: %w", err)
	}
	if err := m.CordonNode(ctx, id, name, true, actingUser, actingGroup); err != nil {
		return DrainResult{}, err
	}
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name})
	if err != nil {
		return DrainResult{}, fmt.Errorf("k8s: failed to list the node's pods: %w", err)
	}
	var res DrainResult
	for _, p := range pods.Items {
		key := p.Namespace + "/" + p.Name
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if isDaemonSetPod(&p) || isMirrorPod(&p) {
			res.Skipped = append(res.Skipped, key)
			continue
		}
		ev := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}}
		if err := client.PolicyV1().Evictions(p.Namespace).Evict(ctx, ev); err != nil {
			reason := err.Error()
			if apierrors.IsTooManyRequests(err) {
				reason = "blocked by PodDisruptionBudget"
			}
			res.Failed = append(res.Failed, key+": "+reason)
			continue
		}
		res.Evicted = append(res.Evicted, key)
	}
	return res, nil
}

func isDaemonSetPod(p *corev1.Pod) bool {
	for _, o := range p.OwnerReferences {
		if o.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func isMirrorPod(p *corev1.Pod) bool {
	_, ok := p.Annotations[corev1.MirrorPodAnnotationKey]
	return ok
}

// SupportsRestart and SupportsScale narrow which kinds support each action, so
// the router can reject with 404 a kind that does not apply.
func SupportsRestart(kind string) bool {
	return kind == "deployments" || kind == "statefulsets" || kind == "daemonsets"
}

func SupportsScale(kind string) bool {
	return kind == "deployments" || kind == "statefulsets"
}
