package api

import (
	"encoding/json"
	"net/http"

	"github.com/bayplan/bayplan/internal/costs"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/settings"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Usage returns the real CPU/memory consumption of the cluster (metrics-server)
// next to what is reserved and allocatable, per node and in total. If
// metrics-server is not available it responds 200 with available=false and the
// reason: the screen keeps painting what is reserved and explains why the
// consumption is missing.
func (h *ResourceHandler) Usage(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	usage, err := h.K8s.ClusterUsage(id)
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

// PodUsage returns the consumption per pod (and container) against requests
// and limits, filterable by namespace like the rest of the listings.
func (h *ResourceHandler) PodUsage(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	list, err := h.K8s.PodUsage(id, parseNamespaces(r))
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// Costs estimates the monthly cost per namespace (see internal/costs). It reads
// the prices from settings.costs; without them it returns the resources with
// cost 0 and configured=false so the UI sends the user to Settings.
func (h *ResourceHandler) Costs(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	list, err := h.K8s.PodUsage(id, parseNamespaces(r))
	if h.handleClusterErr(w, err) {
		return
	}
	var prices settings.Costs
	if h.Settings != nil {
		if err := h.Settings.Get(r.Context(), settings.KeyCosts, &prices); err != nil {
			h.Logger.Error("costs: failed reading prices", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, costs.Aggregate(list, prices))
}

// MetricsServerManifest serves the official components.yaml (with the insecure
// kubelet flag if requested) to apply it with kubectl when the panel cannot:
// GET /api/clusters/{id}/metrics-server/manifest?kubeletInsecureTLS=true.
func (h *ResourceHandler) MetricsServerManifest(w http.ResponseWriter, r *http.Request) {
	y, err := k8s.MetricsServerManifestYAML(r.URL.Query().Get("kubeletInsecureTLS") == "true")
	if err != nil {
		h.Logger.Error("metrics-server: manifest", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="metrics-server-`+k8s.MetricsServerVersion+`.yaml"`)
	_, _ = w.Write([]byte(y))
}

// InstallMetricsServer applies the official manifest with the user's identity
// (admin over the cluster), with a prior dry-run like the YAML editor. If the
// cluster RBAC does not allow creating APIServices or ClusterRoles (scoped mode
// without reapplying), the 502 carries the API server's 403 and the UI offers
// to download the manifest. POST /api/clusters/{id}/metrics-server.
func (h *ResourceHandler) InstallMetricsServer(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in struct {
		DryRun             bool `json:"dryRun"`
		KubeletInsecureTLS bool `json:"kubeletInsecureTLS"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	objs, err := k8s.MetricsServerManifest(in.KubeletInsecureTLS)
	if err != nil {
		h.Logger.Error("metrics-server: manifest", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	// In dry-run, a binding whose role is created in the same manifest cannot be
	// rehearsed: the escalation check looks up the role and in dry-run it does
	// not exist yet ("not found"). Everything else is rehearsed and the affected
	// bindings are assumed "created", with a warning (partialDryRun), just like
	// the quota of a new namespace.
	toApply, assumed := objs, []*unstructured.Unstructured(nil)
	if in.DryRun {
		toApply, assumed = k8s.SplitBindingsToNewRoles(objs)
	}
	results, err := h.K8s.Apply(r.Context(), id, toApply, "kube-system", a.username, a.group, in.DryRun)
	for _, obj := range assumed {
		if err != nil {
			break
		}
		results = append(results, k8s.ApplyResult{Kind: obj.GetKind(), APIVersion: obj.GetAPIVersion(), Namespace: obj.GetNamespace(), Name: obj.GetName(), Operation: "created"})
	}
	if !in.DryRun {
		h.auditAction(r, a, "metrics_server.install", id, "kube-system", "metrics-server", err, map[string]any{"version": k8s.MetricsServerVersion, "kubeletInsecureTLS": in.KubeletInsecureTLS})
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": results, "dryRun": in.DryRun, "partialDryRun": len(assumed) > 0, "version": k8s.MetricsServerVersion})
}
