package k8s

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

// ObjectRef identifies an object by the kind the panel knows (yamlKinds key,
// the same one DeleteResource uses), namespace and name.
type ObjectRef struct {
	KindKey   string `json:"kindKey"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func (r ObjectRef) String() string {
	if r.Namespace == "" {
		return r.Kind + "/" + r.Name
	}
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// ListByLabel returns the objects of ALL the kinds the panel knows (except
// nodes) that carry the given selector. It goes with the technical SA's
// identity, which has list on all of them; whoever deletes afterwards will do
// so with the appropriate impersonated identity. A kind the cluster does not
// know or does not allow listing is skipped: the GitOps prune must not abort
// because an optional CRD is missing.
func (m *Manager) ListByLabel(ctx context.Context, id int64, selector string) ([]ObjectRef, error) {
	conn, err := m.getConn(id)
	if err != nil {
		return nil, err
	}
	cfg := conn.restConfig
	cfg.Impersonate.UserName = ""
	cfg.Impersonate.Groups = nil
	// Discovery takes no context: bounded per request (it never streams).
	disc, err := discovery.NewDiscoveryClientForConfig(boundedConfig(&cfg))
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to create discovery client: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc))
	dyn, err := dynamic.NewForConfig(&cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to create dynamic client: %w", err)
	}
	var out []ObjectRef
	for key, k := range yamlKinds {
		if key == "nodes" {
			continue
		}
		gvk := schema.FromAPIVersionAndKind(k.apiVersion, k.kind)
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			continue
		}
		list, err := dyn.Resource(mapping.Resource).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				continue
			}
			return nil, fmt.Errorf("k8s: failed to list %s: %w", k.kind, err)
		}
		for _, it := range list.Items {
			out = append(out, ObjectRef{KindKey: key, Kind: k.kind, Namespace: it.GetNamespace(), Name: it.GetName()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// KindKeyFor returns the yamlKinds key of an apiVersion/kind, or "" if the
// panel does not know that kind (and therefore cannot delete it in a prune).
func KindKeyFor(apiVersion, kind string) string {
	for key, k := range yamlKinds {
		if k.apiVersion == apiVersion && k.kind == kind {
			return key
		}
	}
	return ""
}
