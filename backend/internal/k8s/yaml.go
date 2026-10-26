package k8s

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// redactedMarker replaces a Secret's values for users without the admin role
// on the cluster. In data (base64 in the YAML) it shows up encoded, so a text
// that is recognizable once decoded is chosen.
const redactedMarker = "<redacted>"

// ErrResourceNotFound is returned when the requested resource is not (or no
// longer) in the cluster's informer cache.
var ErrResourceNotFound = errors.New("k8s: resource not found")

// ErrUnsupportedKind is returned when a kind the panel does not cache is requested.
var ErrUnsupportedKind = errors.New("k8s: unsupported resource kind")

// yamlKind describes how to get an object of a given kind from the cache.
type yamlKind struct {
	apiVersion string
	kind       string
	namespaced bool
	// optional is the name of the optional informer that serves it ("" if it is
	// one of the required ones).
	optional string
	get      func(c *clusterConn, namespace, name string) (runtime.Object, error)
}

// yamlKinds is the table of kinds whose YAML can be viewed. The key is the API
// path segment (lowercase plural, like kubectl).
var yamlKinds = map[string]yamlKind{
	"pods": {"v1", "Pod", true, "", func(c *clusterConn, ns, n string) (runtime.Object, error) { return c.podLister.Pods(ns).Get(n) }},
	"deployments": {"apps/v1", "Deployment", true, "", func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.deploymentLister.Deployments(ns).Get(n)
	}},
	"statefulsets": {"apps/v1", "StatefulSet", true, "", func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.statefulSetLister.StatefulSets(ns).Get(n)
	}},
	"daemonsets": {"apps/v1", "DaemonSet", true, "", func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.daemonSetLister.DaemonSets(ns).Get(n)
	}},
	"services":   {"v1", "Service", true, "", func(c *clusterConn, ns, n string) (runtime.Object, error) { return c.serviceLister.Services(ns).Get(n) }},
	"nodes":      {"v1", "Node", false, "", func(c *clusterConn, _, n string) (runtime.Object, error) { return c.nodeLister.Get(n) }},
	"namespaces": {"v1", "Namespace", false, "", func(c *clusterConn, _, n string) (runtime.Object, error) { return c.namespaceLister.Get(n) }},
	KindResourceQuotas: {"v1", "ResourceQuota", true, KindResourceQuotas, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.resourceQuotas.ResourceQuotas(ns).Get(n)
	}},
	KindLimitRanges: {"v1", "LimitRange", true, KindLimitRanges, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.limitRanges.LimitRanges(ns).Get(n)
	}},
	KindConfigMaps: {"v1", "ConfigMap", true, KindConfigMaps, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.configMaps.ConfigMaps(ns).Get(n)
	}},
	KindSecrets: {"v1", "Secret", true, KindSecrets, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.secrets.Secrets(ns).Get(n)
	}},
	KindServiceAccounts: {"v1", "ServiceAccount", true, KindServiceAccounts, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.serviceAccounts.ServiceAccounts(ns).Get(n)
	}},
	KindPVCs: {"v1", "PersistentVolumeClaim", true, KindPVCs, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.pvcs.PersistentVolumeClaims(ns).Get(n)
	}},
	KindPVs: {"v1", "PersistentVolume", false, KindPVs, func(c *clusterConn, _, n string) (runtime.Object, error) {
		return c.opt.pvs.Get(n)
	}},
	KindIngresses: {"networking.k8s.io/v1", "Ingress", true, KindIngresses, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.ingresses.Ingresses(ns).Get(n)
	}},
	KindNetworkPolicies: {"networking.k8s.io/v1", "NetworkPolicy", true, KindNetworkPolicies, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.networkPolicies.NetworkPolicies(ns).Get(n)
	}},
	KindStorageClasses: {"storage.k8s.io/v1", "StorageClass", false, KindStorageClasses, func(c *clusterConn, _, n string) (runtime.Object, error) {
		return c.opt.storageClasses.Get(n)
	}},
	KindJobs: {"batch/v1", "Job", true, KindJobs, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.jobs.Jobs(ns).Get(n)
	}},
	KindCronJobs: {"batch/v1", "CronJob", true, KindCronJobs, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.cronJobs.CronJobs(ns).Get(n)
	}},
	KindHPAs: {"autoscaling/v2", "HorizontalPodAutoscaler", true, KindHPAs, func(c *clusterConn, ns, n string) (runtime.Object, error) {
		return c.opt.hpas.HorizontalPodAutoscalers(ns).Get(n)
	}},
}

// IsNamespaced says whether a supported kind is namespaced. Error if it is not
// supported.
func IsNamespaced(kind string) (bool, error) {
	k, ok := yamlKinds[kind]
	if !ok {
		return false, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	return k.namespaced, nil
}

// ResourceYAML serializes to YAML a resource already cached by the informer. If
// redactSecrets is true and the kind is Secret, the data values are replaced by
// a marker: a readonly user can see WHICH secrets exist and their keys, but the
// values are only seen by a cluster admin (who could already read them with a
// terminal).
func (m *Manager) ResourceYAML(id int64, kind, namespace, name string, redactSecrets bool) (string, error) {
	k, ok := yamlKinds[kind]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	if kind == KindSecrets && m.ProtectedNamespace(id) != "" && namespace == m.ProtectedNamespace(id) {
		redactSecrets = true // the panel's own Secrets are never shown in clear text
	}
	var conn *clusterConn
	var err error
	if k.optional != "" {
		conn, err = m.getOptionalConn(id, k.optional)
	} else {
		conn, err = m.getSyncedConn(id)
	}
	if err != nil {
		return "", err
	}
	if !k.namespaced {
		namespace = ""
	}
	obj, err := k.get(conn, namespace, name)
	if err != nil {
		return "", translateNotFound(err)
	}

	// The object is ALWAYS copied before touching anything: the listers return
	// the pointer shared with the informer's cache, and clearing ManagedFields
	// or setting TypeMeta on that pointer would corrupt the cache for any other
	// concurrent reader. TypeMeta has to be set by hand: objects coming out of
	// the lister arrive with empty Kind/APIVersion (client-go does not fill
	// them in watch/list responses), and without this the YAML shown would not
	// resemble that of "kubectl get -o yaml".
	copied := obj.DeepCopyObject()
	if accessor, err := metav1Accessor(copied); err == nil {
		accessor.SetManagedFields(nil)
	}
	copied.GetObjectKind().SetGroupVersionKind(schema.FromAPIVersionAndKind(k.apiVersion, k.kind))
	if redactSecrets && kind == KindSecrets {
		if sec, ok := copied.(*corev1.Secret); ok {
			redactSecret(sec)
		}
	}

	b, err := yaml.Marshal(copied)
	if err != nil {
		return "", fmt.Errorf("k8s: failed to serialize %s/%s to YAML: %w", kind, name, err)
	}
	return string(b), nil
}

func metav1Accessor(obj runtime.Object) (metav1.Object, error) {
	accessor, ok := obj.(metav1.Object)
	if !ok {
		return nil, errors.New("no metadata")
	}
	return accessor, nil
}

func translateNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return ErrResourceNotFound
	}
	return fmt.Errorf("k8s: failed to read resource: %w", err)
}
