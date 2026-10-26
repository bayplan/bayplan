package k8s

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// RedactedAnnotation marks a Secret exported without its values.
const RedactedAnnotation = "bayplan.io/redacted"

// DumpItem is an object exported for a configuration backup.
type DumpItem struct {
	Kind      string // the panel's plural (deployments, configmaps…)
	Namespace string // empty if cluster-scoped
	Name      string
	YAML      string
}

// DumpOptions narrows the export.
type DumpOptions struct {
	// Empty Namespaces = all (and then the cluster-scoped objects too:
	// namespaces, storageclasses, persistentvolumes).
	Namespaces []string
	// IncludeSecrets: Secrets go with their values. If false, they go with the
	// values redacted (useful to see what was there, not to restore).
	IncludeSecrets bool
}

// dumpKinds are the types that make up a configuration backup, in the order in
// which they should be restored (namespaces and quotas before what goes inside
// them). Deliberately left out: pods, replicasets and events (runtime state
// that controllers recreate), nodes (they are not "applied").
var dumpKinds = []string{
	"namespaces", KindStorageClasses, KindPVs, KindResourceQuotas, KindLimitRanges,
	KindServiceAccounts, KindConfigMaps, KindSecrets, KindPVCs, KindNetworkPolicies,
	"services", "deployments", "statefulsets", "daemonsets", KindJobs, KindCronJobs, KindHPAs, KindIngresses,
}

// DumpResources exports a cluster's configuration from the informer cache,
// stripped of status fields so it can be re-applied. An optional type without
// permission is skipped (and returned in `skipped` with the reason) instead of
// aborting the backup: a backup without ingresses is better than no backup.
func (m *Manager) DumpResources(id int64, opts DumpOptions) (items []DumpItem, skipped map[string]string, err error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, nil, err
	}
	skipped = map[string]string{}
	protected := m.ProtectedNamespace(id)
	if protected != "" {
		skipped["namespace/"+protected] = "the panel's own namespace: not backed up"
	}
	all := len(opts.Namespaces) == 0
	nsSet := map[string]bool{}
	for _, ns := range opts.Namespaces {
		nsSet[ns] = true
	}
	for _, kind := range dumpKinds {
		k, ok := yamlKinds[kind]
		if !ok {
			continue
		}
		if !k.namespaced && !all {
			continue
		}
		if k.optional != "" {
			if inf, ok := conn.optional[k.optional]; !ok || !inf.HasSynced() {
				msg := conn.optionalError(k.optional)
				if msg == "" {
					msg = "informer not synced"
				}
				skipped[kind] = msg
				continue
			}
		}
		objs, err := listAll(conn, kind)
		if err != nil {
			skipped[kind] = err.Error()
			continue
		}
		for _, obj := range objs {
			acc, err := metav1Accessor(obj)
			if err != nil {
				continue
			}
			if k.namespaced && !all && !nsSet[acc.GetNamespace()] {
				continue
			}
			// The panel's own namespace is not backed up: its Secrets are the
			// encryption key and the DB, and a backup is downloadable by an admin.
			if k.namespaced && protected != "" && acc.GetNamespace() == protected {
				continue
			}
			// The `default` ServiceAccounts and the token/Helm Secrets are managed
			// by the cluster: restoring them would cause conflicts or duplicates.
			if kind == KindServiceAccounts && acc.GetName() == "default" {
				continue
			}
			if kind == KindSecrets {
				if sec, ok := obj.(*corev1.Secret); ok && (sec.Type == corev1.SecretTypeServiceAccountToken || sec.Type == "helm.sh/release.v1") {
					continue
				}
			}
			if kind == "namespaces" && isSystemNamespace(acc.GetName()) {
				continue
			}
			y, err := cleanedYAML(obj, k, kind == KindSecrets && !opts.IncludeSecrets)
			if err != nil {
				continue
			}
			ns := ""
			if k.namespaced {
				ns = acc.GetNamespace()
			}
			items = append(items, DumpItem{Kind: kind, Namespace: ns, Name: acc.GetName(), YAML: y})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Namespace != items[j].Namespace {
			return items[i].Namespace < items[j].Namespace
		}
		return items[i].Name < items[j].Name
	})
	return items, skipped, nil
}

// isSystemNamespace: the namespaces every cluster ships with are not exported
// (there is nothing to restore and applying them to another cluster is noise).
func isSystemNamespace(name string) bool {
	switch name {
	case "default", "kube-system", "kube-public", "kube-node-lease":
		return true
	}
	return false
}

// listAll returns all objects of a type from its lister.
func listAll(conn *clusterConn, kind string) ([]runtime.Object, error) {
	everything := labels.Everything()
	wrap := func(err error, list []runtime.Object) ([]runtime.Object, error) {
		if err != nil {
			return nil, err
		}
		return list, nil
	}
	switch kind {
	case "namespaces":
		l, err := conn.namespaceLister.List(everything)
		return wrap(err, toObjects(l))
	case "deployments":
		l, err := conn.deploymentLister.List(everything)
		return wrap(err, toObjects(l))
	case "statefulsets":
		l, err := conn.statefulSetLister.List(everything)
		return wrap(err, toObjects(l))
	case "daemonsets":
		l, err := conn.daemonSetLister.List(everything)
		return wrap(err, toObjects(l))
	case "services":
		l, err := conn.serviceLister.List(everything)
		return wrap(err, toObjects(l))
	case KindConfigMaps:
		l, err := conn.opt.configMaps.List(everything)
		return wrap(err, toObjects(l))
	case KindSecrets:
		l, err := conn.opt.secrets.List(everything)
		return wrap(err, toObjects(l))
	case KindServiceAccounts:
		l, err := conn.opt.serviceAccounts.List(everything)
		return wrap(err, toObjects(l))
	case KindPVCs:
		l, err := conn.opt.pvcs.List(everything)
		return wrap(err, toObjects(l))
	case KindPVs:
		l, err := conn.opt.pvs.List(everything)
		return wrap(err, toObjects(l))
	case KindIngresses:
		l, err := conn.opt.ingresses.List(everything)
		return wrap(err, toObjects(l))
	case KindNetworkPolicies:
		l, err := conn.opt.networkPolicies.List(everything)
		return wrap(err, toObjects(l))
	case KindStorageClasses:
		l, err := conn.opt.storageClasses.List(everything)
		return wrap(err, toObjects(l))
	case KindJobs:
		l, err := conn.opt.jobs.List(everything)
		return wrap(err, toObjects(l))
	case KindCronJobs:
		l, err := conn.opt.cronJobs.List(everything)
		return wrap(err, toObjects(l))
	case KindHPAs:
		l, err := conn.opt.hpas.List(everything)
		return wrap(err, toObjects(l))
	case KindResourceQuotas:
		l, err := conn.opt.resourceQuotas.List(everything)
		return wrap(err, toObjects(l))
	case KindLimitRanges:
		l, err := conn.opt.limitRanges.List(everything)
		return wrap(err, toObjects(l))
	}
	return nil, fmt.Errorf("k8s: kind %q is not exportable", kind)
}

func toObjects[T runtime.Object](list []T) []runtime.Object {
	out := make([]runtime.Object, 0, len(list))
	for _, o := range list {
		out = append(out, o)
	}
	return out
}

// cleanedYAML serializes an object without what the API server fills in and
// what would block or pollute a later apply (status, uid, resourceVersion,
// managedFields…). It is the same thing `kubectl get -o yaml` plus
// `kubectl-neat` do. Jobs additionally lose the selector/labels the controller
// adds, because a Job cannot be recreated with them.
func cleanedYAML(obj runtime.Object, k yamlKind, redactSecret bool) (string, error) {
	copied := obj.DeepCopyObject()
	copied.GetObjectKind().SetGroupVersionKind(schema.FromAPIVersionAndKind(k.apiVersion, k.kind))
	raw, err := yaml.Marshal(copied)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	delete(m, "status")
	if redactSecret {
		// A redacted Secret keeps the KEYS (to know what was there) but not the
		// values, in stringData and in clear text: that way it shows in the YAML
		// and Restore recognizes it by the annotation and rejects it. In `data`
		// it would be base64 and would pass for a real value.
		keys := map[string]any{}
		if d, ok := m["data"].(map[string]any); ok {
			for key := range d {
				keys[key] = redactedMarker
			}
		}
		if d, ok := m["stringData"].(map[string]any); ok {
			for key := range d {
				keys[key] = redactedMarker
			}
		}
		delete(m, "data")
		m["stringData"] = keys
		meta, _ := m["metadata"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
			m["metadata"] = meta
		}
		ann, _ := meta["annotations"].(map[string]any)
		if ann == nil {
			ann = map[string]any{}
		}
		for k, v := range ann {
			if s, _ := v.(string); IsSecretCopyAnnotation(k, s) {
				delete(ann, k)
			}
		}
		ann[RedactedAnnotation] = "true"
		meta["annotations"] = ann
	}
	if meta, ok := m["metadata"].(map[string]any); ok {
		for _, f := range []string{"managedFields", "uid", "resourceVersion", "generation", "creationTimestamp", "selfLink", "ownerReferences"} {
			delete(meta, f)
		}
		if ann, ok := meta["annotations"].(map[string]any); ok {
			delete(ann, "kubectl.kubernetes.io/last-applied-configuration")
			delete(ann, "deployment.kubernetes.io/revision")
			if len(ann) == 0 {
				delete(meta, "annotations")
			}
		}
		// An object that had an ownerReference annotation or similar has already
		// been cleaned above; nothing more to do here.
	}
	if k.kind == "Job" {
		if spec, ok := m["spec"].(map[string]any); ok {
			delete(spec, "selector")
			if tpl, ok := spec["template"].(map[string]any); ok {
				if tm, ok := tpl["metadata"].(map[string]any); ok {
					if lbl, ok := tm["labels"].(map[string]any); ok {
						delete(lbl, "controller-uid")
						delete(lbl, "batch.kubernetes.io/controller-uid")
						delete(lbl, "job-name")
						delete(lbl, "batch.kubernetes.io/job-name")
					}
				}
			}
		}
	}
	if k.kind == "Service" {
		if spec, ok := m["spec"].(map[string]any); ok {
			// IPs assigned by the cluster do not survive another cluster.
			delete(spec, "clusterIP")
			delete(spec, "clusterIPs")
		}
	}
	if k.kind == "PersistentVolumeClaim" {
		if spec, ok := m["spec"].(map[string]any); ok {
			delete(spec, "volumeName")
		}
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
