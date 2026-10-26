// Package api contains the panel's REST HTTP handlers.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/k8s"
)

// ReadinessSource is implemented by k8s.Manager to report the status of its
// informers to /readyz. It may be nil if the manager has not been built yet.
type ReadinessSource interface {
	ClusterReadiness() []k8s.ClusterReadiness
}

// HealthHandler serves /healthz (liveness) and /readyz (readiness).
type HealthHandler struct {
	DB      *pgxpool.Pool
	Logger  *slog.Logger
	Sources ReadinessSource // may be nil until the multi-cluster manager exists
}

// Healthz answers 200 if the process is alive, without checking external
// dependencies. Use it for liveness probes / container restarts.
func (h *HealthHandler) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz answers 200 if the database responds. The clusters' status is included
// as information but does NOT affect the response code: a managed cluster being
// down is a fact the UI shows, not a reason to pull the panel itself out of the
// load balancer (a single unreachable cluster used to leave the whole
// application "not ready").
func (h *HealthHandler) Readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	resp := map[string]any{}
	ready := true

	if err := h.DB.Ping(ctx); err != nil {
		ready = false
		resp["db"] = "error: " + err.Error()
		h.Logger.Warn("readyz: database ping failed", "error", err)
	} else {
		resp["db"] = "ok"
	}

	if h.Sources != nil {
		// Unauthenticated: the sync flag per cluster, like the Prometheus
		// gauge, but not the last error (it quotes the API server's hostname
		// and its RBAC messages).
		list := h.Sources.ClusterReadiness()
		for i := range list {
			list[i].LastError = ""
		}
		resp["clusters"] = list
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	resp["ready"] = ready
	writeJSON(w, status, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
