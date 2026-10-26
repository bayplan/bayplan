package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/bayplan/bayplan/internal/k8s"
)

var namespaceNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// namespaceQuotaInput is the "namespace quota" form: in a single operation the
// Namespace (if it does not exist), a ResourceQuota with the caps and a
// LimitRange with the per-container defaults. It is the multi-tenant
// "project" reduced to what Kubernetes already knows how to enforce; no
// namespace groupings of the panel's own.
//
// Each field is a Kubernetes quantity ("2", "500m", "4Gi") or empty to not set
// that cap. If there is no cap at all the ResourceQuota is not created; if
// there is no default at all the LimitRange is not created.
type namespaceQuotaInput struct {
	CreateNamespace bool `json:"createNamespace"`
	// Namespace caps (ResourceQuota bayplan-quota).
	RequestsCPU     string `json:"requestsCpu"`
	RequestsMemory  string `json:"requestsMemory"`
	LimitsCPU       string `json:"limitsCpu"`
	LimitsMemory    string `json:"limitsMemory"`
	Pods            string `json:"pods"`
	PVCs            string `json:"persistentVolumeClaims"`
	RequestsStorage string `json:"requestsStorage"`
	// Per-container defaults (LimitRange bayplan-limits), so that pods that do
	// not declare resources count against the quota.
	DefaultRequestCPU    string `json:"defaultRequestCpu"`
	DefaultRequestMemory string `json:"defaultRequestMemory"`
	DefaultLimitCPU      string `json:"defaultLimitCpu"`
	DefaultLimitMemory   string `json:"defaultLimitMemory"`
	MaxCPU               string `json:"maxCpu"`
	MaxMemory            string `json:"maxMemory"`
	DryRun               bool   `json:"dryRun"`
}

// quantity validates a quantity; empty = do not set.
func quantity(field, v string, into map[string]any, key string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return &ValidationError{Msg: field + ": invalid quantity " + strings.TrimSpace(v) + " (examples: 2, 500m, 4Gi)"}
	}
	into[key] = q.String()
	return nil
}

// ApplyNamespaceQuota: POST /api/clusters/{id}/namespaces/{namespace}/quota.
func (h *ResourceHandler) ApplyNamespaceQuota(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ns := r.PathValue("namespace")
	if !namespaceNameRe.MatchString(ns) {
		http.Error(w, "invalid namespace", http.StatusBadRequest)
		return
	}
	var in namespaceQuotaInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	hard := map[string]any{}
	for _, f := range []struct{ field, v, key string }{
		{"requestsCpu", in.RequestsCPU, "requests.cpu"}, {"requestsMemory", in.RequestsMemory, "requests.memory"},
		{"limitsCpu", in.LimitsCPU, "limits.cpu"}, {"limitsMemory", in.LimitsMemory, "limits.memory"},
		{"pods", in.Pods, "pods"}, {"persistentVolumeClaims", in.PVCs, "persistentvolumeclaims"}, {"requestsStorage", in.RequestsStorage, "requests.storage"},
	} {
		if err := quantity(f.field, f.v, hard, f.key); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	defReq, defLim, maxes := map[string]any{}, map[string]any{}, map[string]any{}
	for _, f := range []struct {
		field, v string
		into     map[string]any
		key      string
	}{
		{"defaultRequestCpu", in.DefaultRequestCPU, defReq, "cpu"}, {"defaultRequestMemory", in.DefaultRequestMemory, defReq, "memory"},
		{"defaultLimitCpu", in.DefaultLimitCPU, defLim, "cpu"}, {"defaultLimitMemory", in.DefaultLimitMemory, defLim, "memory"},
		{"maxCpu", in.MaxCPU, maxes, "cpu"}, {"maxMemory", in.MaxMemory, maxes, "memory"},
	} {
		if err := quantity(f.field, f.v, f.into, f.key); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if len(hard) == 0 && len(defReq) == 0 && len(defLim) == 0 && len(maxes) == 0 && !in.CreateNamespace {
		http.Error(w, "specify at least one cap or one default value", http.StatusBadRequest)
		return
	}

	labels := map[string]any{"app.kubernetes.io/managed-by": "bayplan"}
	var objs []*unstructured.Unstructured
	if in.CreateNamespace {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": ns},
		}})
	}
	if len(hard) > 0 {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ResourceQuota",
			"metadata": map[string]any{"name": k8s.QuotaObjectName, "namespace": ns, "labels": labels},
			"spec":     map[string]any{"hard": hard},
		}})
	}
	if len(defReq) > 0 || len(defLim) > 0 || len(maxes) > 0 {
		item := map[string]any{"type": "Container"}
		if len(defReq) > 0 {
			item["defaultRequest"] = defReq
		}
		if len(defLim) > 0 {
			item["default"] = defLim
		}
		if len(maxes) > 0 {
			item["max"] = maxes
		}
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "LimitRange",
			"metadata": map[string]any{"name": k8s.LimitRangeObjectName, "namespace": ns, "labels": labels},
			"spec":     map[string]any{"limits": []any{item}},
		}})
	}

	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	// A server dry-run creates nothing, so the ResourceQuota and the LimitRange
	// of a namespace that does not exist yet fail with "not found" even though
	// the real apply is going to work. In that case only the Namespace is
	// rehearsed and the other two are assumed "created", warning the UI.
	toApply, assumed := objs, []*unstructured.Unstructured(nil)
	if in.DryRun && in.CreateNamespace && !h.namespaceExists(id, ns) {
		toApply, assumed = objs[:1], objs[1:]
	}
	results, err := h.K8s.Apply(r.Context(), id, toApply, ns, a.username, a.group, in.DryRun)
	for _, obj := range assumed {
		if err != nil {
			break
		}
		results = append(results, k8s.ApplyResult{Kind: obj.GetKind(), APIVersion: obj.GetAPIVersion(), Namespace: ns, Name: obj.GetName(), Operation: "created"})
	}
	if !in.DryRun {
		h.auditAction(r, a, "namespace.quota", id, ns, k8s.QuotaObjectName, err, map[string]any{"objects": len(objs), "hard": hard, "createNamespace": in.CreateNamespace})
	}
	if err != nil {
		// The API server's error verbatim: RBAC, admission, quota already violated...
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": results, "dryRun": in.DryRun, "partialDryRun": len(assumed) > 0})
}

// namespaceExists queries the informer cache; if the cluster is not synced it
// is assumed to exist and the full dry-run will say whatever it says.
func (h *ResourceHandler) namespaceExists(id int64, name string) bool {
	nss, err := h.K8s.ListNamespaces(id)
	if err != nil {
		return true
	}
	for _, n := range nss {
		if n.Name == name {
			return true
		}
	}
	return false
}
