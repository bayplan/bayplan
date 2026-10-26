package api

import (
	"net/http"
	"time"
)

// Overview returns the summary of a cluster: counters per state, reserved
// capacity, kubelet versions, the prioritized "needs attention" list and the
// latest Warning events. Everything comes from the informer cache in a single
// request, instead of forcing the UI to fetch five full listings and aggregate
// them in the browser.
//
// The "now" is passed explicitly to the k8s layer: the dwell thresholds (a
// Pending pod only counts if it has been so for a while) are pure, testable
// logic precisely because the clock is a parameter and not a call hidden
// inside.
func (h *ResourceHandler) Overview(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	overview, err := h.K8s.ClusterOverview(id, time.Now())
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

// Topology returns the graph of the selected namespaces (Ingress -> Service ->
// workload -> pods -> PVC) from the informer cache. Like Overview, the clock is
// passed in because node statuses reuse the attention grace periods.
func (h *ResourceHandler) Topology(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	topology, err := h.K8s.ClusterTopology(id, parseNamespaces(r), time.Now())
	if h.handleClusterErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, topology)
}
