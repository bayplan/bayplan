package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/settings"
)

// ResourceHandler exposes reads (from the k8s.Manager informer cache) and
// mutating actions (restart/scale, straight to the API server and
// impersonating the real user) over the managed clusters.
type ResourceHandler struct {
	DB       *pgxpool.Pool
	Logger   *slog.Logger
	K8s      *k8s.Manager
	Settings *settings.Store // cost prices; nil = not configured
}

func parseClusterID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// parseNamespaces reads the namespace filter, which accepts a comma-separated
// list (`?namespace=apps-pro,kasten-io`). Empty list = all.
//
// The comma is safe as a separator because a namespace name is a DNS-1123
// label (lowercase, digits and hyphens), so it can never contain one. It was
// chosen over the repeated parameter because it leaves `useUrlState` untouched
// in the frontend and avoids axios' array serializer, which generates
// `namespace[]=a&namespace[]=b` — a form Go does not read.
//
// Trims and deduplicates: a name with spaces would not match any namespace and
// the filter would return an empty list with nothing explaining why. A single
// value keeps working as before, so existing links do not break.
func parseNamespaces(r *http.Request) []string {
	raw := r.URL.Query().Get("namespace")
	if raw == "" {
		return nil
	}
	seen := make(map[string]bool)
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// handleClusterErr writes the appropriate error response and returns true if
// there was an error to handle (for the `if h.handleErr(w, err) { return }`
// pattern).
func (h *ResourceHandler) handleClusterErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, k8s.ErrClusterNotFound) {
		http.Error(w, "cluster not found or not connected", http.StatusNotFound)
		return true
	}
	// 503 and not 404: the cluster exists and this fixes itself once the
	// informer syncs, so the right answer is "try again", not "does not exist".
	// And above all an empty list is NOT returned: an unreachable cluster would
	// look identical to a cluster with no resources.
	if errors.Is(err, k8s.ErrClusterNotSynced) {
		http.Error(w, "the cluster is not synced yet, try again in a few seconds", http.StatusServiceUnavailable)
		return true
	}
	if errors.Is(err, k8s.ErrResourceNotFound) {
		http.Error(w, "resource not found", http.StatusNotFound)
		return true
	}
	h.Logger.Error("resources: failed querying cluster", "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
	return true
}

func (h *ResourceHandler) Namespaces(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ns, err := h.K8s.ListNamespaces(id)
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

func (h *ResourceHandler) Pods(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	pods, err := h.K8s.ListPods(id, parseNamespaces(r))
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, pods)
}

func (h *ResourceHandler) Deployments(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	deployments, err := h.K8s.ListDeployments(id, parseNamespaces(r))
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

func (h *ResourceHandler) StatefulSets(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	sets, err := h.K8s.ListStatefulSets(id, parseNamespaces(r))
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, sets)
}

func (h *ResourceHandler) DaemonSets(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	sets, err := h.K8s.ListDaemonSets(id, parseNamespaces(r))
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, sets)
}

// DeploymentPods returns the pods of a specific deployment, resolved through
// ownerReferences via its ReplicaSets.
func (h *ResourceHandler) DeploymentPods(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	pods, err := h.K8s.ListDeploymentPods(id, r.PathValue("namespace"), r.PathValue("name"))
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, pods)
}

func (h *ResourceHandler) Services(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	services, err := h.K8s.ListServices(id, parseNamespaces(r))
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, services)
}

func (h *ResourceHandler) Nodes(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	nodes, err := h.K8s.ListNodes(id)
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

// Summary returns aggregated counters (pods/deployments/nodes and how many are
// not OK) for the dashboard's cluster card.
func (h *ResourceHandler) Summary(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	summary, err := h.K8s.ClusterSummary(id)
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// maxEventsReturned limits the /events payload; see the "no silent caps" note —
// the limit is fixed and known, not a surprise truncation.
const maxEventsReturned = 200

// eventsResponse is the shape returned by /events: besides the events, it says
// whether the result was truncated to maxEventsReturned after applying the
// filter, so the UI does not suggest it has seen the whole history.
type eventsResponse struct {
	Items     []k8s.EventSummary `json:"items"`
	Truncated bool               `json:"truncated"`
}

// parseEventFilter reads the optional event filtering query params. An invalid
// date value is rejected with 400 instead of being silently ignored (prevents
// a typo in the UI from looking like "no filter").
func parseEventFilter(r *http.Request) (k8s.EventFilter, error) {
	q := r.URL.Query()
	filter := k8s.EventFilter{Type: q.Get("type")}
	if since := q.Get("since"); since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return k8s.EventFilter{}, fmt.Errorf("invalid since (RFC3339 expected): %w", err)
		}
		filter.Since = t
	}
	if until := q.Get("until"); until != "" {
		t, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return k8s.EventFilter{}, fmt.Errorf("invalid until (RFC3339 expected): %w", err)
		}
		filter.Until = t
	}
	return filter, nil
}

func (h *ResourceHandler) Events(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	filter, err := parseEventFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := h.K8s.ListEvents(id, parseNamespaces(r), filter, maxEventsReturned)
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, eventsResponse{Items: result.Items, Truncated: result.Truncated})
}

// yamlResponse is the shape of the "view YAML" endpoints.
type yamlResponse struct {
	YAML string `json:"yaml"`
}

// ResourceYAML serves GET /api/clusters/{id}/resources/{kind}/{namespace}/{name}/yaml
// for any cached kind. Non-namespaced kinds use "-" as the namespace in the
// route (a wildcard pattern cannot omit the segment).
//
// The values of a Secret are only returned to whoever has the admin role over
// THAT cluster: a readonly can see which secrets exist and their keys, not
// their content.
func (h *ResourceHandler) ResourceYAML(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	kind := r.PathValue("kind")
	namespace := r.PathValue("namespace")
	if namespace == "-" {
		namespace = ""
	}
	name := r.PathValue("name")

	redact := true
	if kind == k8s.KindSecrets {
		if sess, ok := middleware.SessionFromContext(r.Context()); ok {
			// The role on THIS cluster, already capped for read-only tokens,
			// is the one RequireClusterRole left in the context.
			role, ok := middleware.RoleFromContext(r.Context())
			redact = !ok || !role.IsAdmin()
			if !redact {
				// Reading the value of a secret is a sensitive access: it goes
				// into the audit log just like opening a terminal or reading
				// logs.
				audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
					UserID: &sess.UserID, ActorUsername: sess.Username, IP: middleware.ClientIP(r), Action: "secret.read",
					Cluster: h.K8s.DisplayName(id), Namespace: namespace, Resource: name, Result: audit.ResultSuccess,
				})
			}
		}
	}

	y, err := h.K8s.ResourceYAML(id, kind, namespace, name, redact)
	if errors.Is(err, k8s.ErrUnsupportedKind) {
		http.Error(w, "unsupported resource kind", http.StatusNotFound)
		return
	}
	// Optional kinds (configmaps, secrets…) can fail with
	// ErrInformerNotSynced: 503 with the reason, not a 500.
	if h.handleOptionalErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, yamlResponse{YAML: y})
}

// Old per-kind "view YAML" routes: kept as aliases of the generic endpoint.
func (h *ResourceHandler) PodYAML(w http.ResponseWriter, r *http.Request) {
	h.legacyYAML(w, r, "pods", true)
}
func (h *ResourceHandler) DeploymentYAML(w http.ResponseWriter, r *http.Request) {
	h.legacyYAML(w, r, "deployments", true)
}
func (h *ResourceHandler) ServiceYAML(w http.ResponseWriter, r *http.Request) {
	h.legacyYAML(w, r, "services", true)
}
func (h *ResourceHandler) NodeYAML(w http.ResponseWriter, r *http.Request) {
	h.legacyYAML(w, r, "nodes", false)
}

func (h *ResourceHandler) legacyYAML(w http.ResponseWriter, r *http.Request, kind string, namespaced bool) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	namespace := ""
	if namespaced {
		namespace = r.PathValue("namespace")
	}
	y, err := h.K8s.ResourceYAML(id, kind, namespace, r.PathValue("name"), true)
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, yamlResponse{YAML: y})
}

// --- Optional kinds ---
//
// listOptional centralizes the pattern of the secondary listings. An
// ErrInformerNotSynced is translated to 503 with the reason (usually the
// cluster RBAC not reapplied), and ONLY for that kind.

func (h *ResourceHandler) handleOptionalErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, k8s.ErrInformerNotSynced) {
		http.Error(w, "this resource kind is not available in the cluster: "+err.Error(), http.StatusServiceUnavailable)
		return true
	}
	return h.handleClusterErr(w, err)
}

func namespacedList[T any](h *ResourceHandler, w http.ResponseWriter, r *http.Request, list func(int64, []string) ([]T, error)) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	items, err := list(id, parseNamespaces(r))
	if h.handleOptionalErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func clusterList[T any](h *ResourceHandler, w http.ResponseWriter, r *http.Request, list func(int64) ([]T, error)) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	items, err := list(id)
	if h.handleOptionalErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *ResourceHandler) ConfigMaps(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListConfigMaps)
}
func (h *ResourceHandler) Secrets(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListSecrets)
}
func (h *ResourceHandler) ServiceAccounts(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListServiceAccounts)
}
func (h *ResourceHandler) Ingresses(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListIngresses)
}
func (h *ResourceHandler) NetworkPolicies(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListNetworkPolicies)
}
func (h *ResourceHandler) PVCs(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListPVCs)
}
func (h *ResourceHandler) Jobs(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListJobs)
}
func (h *ResourceHandler) CronJobs(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListCronJobs)
}
func (h *ResourceHandler) HPAs(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListHPAs)
}
func (h *ResourceHandler) ResourceQuotas(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListResourceQuotas)
}
func (h *ResourceHandler) LimitRanges(w http.ResponseWriter, r *http.Request) {
	namespacedList(h, w, r, h.K8s.ListLimitRanges)
}
func (h *ResourceHandler) PVs(w http.ResponseWriter, r *http.Request) {
	clusterList(h, w, r, h.K8s.ListPVs)
}
func (h *ResourceHandler) StorageClasses(w http.ResponseWriter, r *http.Request) {
	clusterList(h, w, r, h.K8s.ListStorageClasses)
}

// Kinds returns, per optional kind, whether it is available in the cluster and
// if not why: the UI paints the missing tabs with the reason instead of an
// empty table or an opaque 503.
func (h *ResourceHandler) Kinds(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	kinds := h.K8s.OptionalKinds(id)
	if kinds == nil {
		http.Error(w, "cluster not found or not connected", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, kinds)
}
