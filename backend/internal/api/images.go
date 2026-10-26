package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/scan"
)

// ImagesHandler: per-cluster image inventory and Trivy scanning.
type ImagesHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager
	Scan   *scan.Service
}

type imageRow struct {
	k8s.ImageUsage
	Scan *scan.Record `json:"scan,omitempty"`
}

// clusterErr maps the cluster errors the same way as the resource listings
// (404 cluster not found, 503 not synced, 500 otherwise).
func (h *ImagesHandler) clusterErr(w http.ResponseWriter, err error) bool {
	return (&ResourceHandler{Logger: h.Logger}).handleClusterErr(w, err)
}

// List joins the images in use with their scans.
func (h *ImagesHandler) List(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	images, err := h.K8s.ClusterImages(id, parseNamespaces(r))
	if h.clusterErr(w, err) {
		return
	}
	refs := make([]string, 0, len(images))
	for _, im := range images {
		refs = append(refs, im.Image)
	}
	scans, err := h.Scan.Store.ForImages(r.Context(), refs)
	if err != nil {
		h.Logger.Error("images.list: failed reading scans", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]imageRow, 0, len(images))
	for _, im := range images {
		row := imageRow{ImageUsage: im}
		if s, ok := scans[im.Image]; ok {
			s := s
			row.Scan = &s
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// Status: scanner availability and queue.
func (h *ImagesHandler) Status(w http.ResponseWriter, r *http.Request) {
	queued, _ := h.Scan.Store.Queued(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"available": h.Scan.Runner.Available(), "version": h.Scan.Runner.Version(), "queued": queued})
}

// Enqueue queues one image or all the images of the cluster (filtered by namespace).
func (h *ImagesHandler) Enqueue(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if !h.Scan.Runner.Available() {
		http.Error(w, scan.ErrUnavailable.Error(), http.StatusNotFound)
		return
	}
	var in struct {
		Image string `json:"image"`
		All   bool   `json:"all"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	sess, _ := middleware.SessionFromContext(r.Context())
	var refs []string
	if in.All {
		images, err := h.K8s.ClusterImages(id, parseNamespaces(r))
		if h.clusterErr(w, err) {
			return
		}
		for _, im := range images {
			refs = append(refs, im.Image)
		}
	} else {
		in.Image = strings.TrimSpace(in.Image)
		if in.Image == "" {
			http.Error(w, "specify image or all", http.StatusBadRequest)
			return
		}
		// Only images running in THIS cluster: the queue is not a generic
		// internet scanner.
		images, err := h.K8s.ClusterImages(id, nil)
		if h.clusterErr(w, err) {
			return
		}
		found := false
		for _, im := range images {
			if im.Image == in.Image {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "that image is not in use in the cluster", http.StatusNotFound)
			return
		}
		refs = []string{in.Image}
	}
	queued := 0
	for _, ref := range refs {
		ok, err := h.Scan.Enqueue(r.Context(), ref, sess.Username, in.Force)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if ok {
			queued++
		}
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, ActorUsername: sess.Username, IP: middleware.ClientIP(r), Action: "image.scan",
		Cluster: h.K8s.DisplayName(id), Result: audit.ResultSuccess, Detail: map[string]any{"images": len(refs), "queued": queued, "all": in.All},
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"requested": len(refs), "queued": queued})
}

// Report returns the full report of an image.
func (h *ImagesHandler) Report(w http.ResponseWriter, r *http.Request) {
	image := strings.TrimSpace(r.URL.Query().Get("image"))
	if image == "" {
		http.Error(w, "missing image", http.StatusBadRequest)
		return
	}
	rec, err := h.Scan.Store.Get(r.Context(), image)
	if errors.Is(err, scan.ErrNotFound) {
		http.Error(w, "that image has not been scanned", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}
