package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/dockerenv"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
)

// DockerHandler exposes a Docker/Podman environment connected via agent. The
// routes hang off /api/clusters/{id}/docker/... and use the same per-cluster
// RBAC: readonly lists and views logs; editor starts/stops/restarts and pulls;
// admin removes containers, images, volumes and networks and opens a terminal.
type DockerHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager // DisplayName for auditing
	Docker *dockerenv.Manager
}

func (h *DockerHandler) client(w http.ResponseWriter, r *http.Request) (*dockerenv.Client, int64, bool) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return nil, 0, false
	}
	c, err := h.Docker.Client(id)
	if err != nil {
		http.Error(w, "the host's agent is not connected", http.StatusServiceUnavailable)
		return nil, 0, false
	}
	return c, id, true
}

func (h *DockerHandler) dockerErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, dockerenv.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, dockerenv.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, dockerenv.ErrHostOffline):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
	return true
}

func (h *DockerHandler) audit(r *http.Request, action string, clusterID int64, resource string, err error, detail map[string]any) {
	sess, _ := middleware.SessionFromContext(r.Context())
	var uid *int64
	var uname string
	if sess != nil {
		uid, uname = &sess.UserID, sess.Username
	}
	result := audit.ResultSuccess
	if err != nil {
		result = audit.ResultFailure
		if detail == nil {
			detail = map[string]any{}
		}
		detail["error"] = err.Error()
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: uid, ActorUsername: uname, IP: middleware.ClientIP(r), Action: action,
		Cluster: h.K8s.DisplayName(clusterID), Resource: resource, Result: result, Detail: detail,
	})
}

func (h *DockerHandler) Info(w http.ResponseWriter, r *http.Request) {
	c, _, ok := h.client(w, r)
	if !ok {
		return
	}
	info, err := c.Info(r.Context())
	if h.dockerErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *DockerHandler) Containers(w http.ResponseWriter, r *http.Request) {
	c, _, ok := h.client(w, r)
	if !ok {
		return
	}
	list, err := c.ListContainers(r.Context(), true)
	if h.dockerErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *DockerHandler) ContainerInspect(w http.ResponseWriter, r *http.Request) {
	c, _, ok := h.client(w, r)
	if !ok {
		return
	}
	raw, _, err := c.ContainerInspect(r.Context(), r.PathValue("cid"))
	if h.dockerErr(w, err) {
		return
	}
	// On a Docker host the credentials live in the environment (there is no
	// Secret object to mask), so Config.Env is treated like a Secret's values:
	// only an admin of the environment sees it. The role in the context is the
	// one for this cluster, already capped for read-only tokens.
	if role, _ := middleware.RoleFromContext(r.Context()); !role.IsAdmin() {
		if masked, err := dockerenv.MaskInspect(raw); err == nil {
			raw = masked
		} else {
			h.Logger.Warn("docker.inspect: could not mask the environment", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// ContainerAction: POST .../containers/{cid}/{action} with action start|stop|restart|kill|pause|unpause.
func (h *DockerHandler) ContainerAction(w http.ResponseWriter, r *http.Request) {
	c, id, ok := h.client(w, r)
	if !ok {
		return
	}
	cid, action := r.PathValue("cid"), r.PathValue("action")
	err := c.ContainerAction(r.Context(), cid, action)
	h.audit(r, "container."+action, id, cid, err, nil)
	if h.dockerErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DockerHandler) RemoveContainer(w http.ResponseWriter, r *http.Request) {
	c, id, ok := h.client(w, r)
	if !ok {
		return
	}
	cid := r.PathValue("cid")
	force := r.URL.Query().Get("force") == "1"
	volumes := r.URL.Query().Get("volumes") == "1"
	err := c.RemoveContainer(r.Context(), cid, force, volumes)
	h.audit(r, "container.remove", id, cid, err, map[string]any{"force": force, "volumes": volumes})
	if h.dockerErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DockerHandler) Images(w http.ResponseWriter, r *http.Request) {
	c, _, ok := h.client(w, r)
	if !ok {
		return
	}
	list, err := c.ListImages(r.Context())
	if h.dockerErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// PullImage: POST .../images/pull {ref}. Synchronous: it waits for the download
// to finish (it can take a while; the client has a long timeout).
func (h *DockerHandler) PullImage(w http.ResponseWriter, r *http.Request) {
	c, id, ok := h.client(w, r)
	if !ok {
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || strings.TrimSpace(body.Ref) == "" {
		http.Error(w, "ref is required", http.StatusBadRequest)
		return
	}
	ref := strings.TrimSpace(body.Ref)
	err := c.PullImage(r.Context(), ref)
	h.audit(r, "image.pull", id, ref, err, nil)
	if h.dockerErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DockerHandler) RemoveImage(w http.ResponseWriter, r *http.Request) {
	c, id, ok := h.client(w, r)
	if !ok {
		return
	}
	iid := r.PathValue("iid")
	err := c.RemoveImage(r.Context(), iid, r.URL.Query().Get("force") == "1")
	h.audit(r, "image.remove", id, iid, err, nil)
	if h.dockerErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DockerHandler) Volumes(w http.ResponseWriter, r *http.Request) {
	c, _, ok := h.client(w, r)
	if !ok {
		return
	}
	list, err := c.ListVolumes(r.Context())
	if h.dockerErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *DockerHandler) RemoveVolume(w http.ResponseWriter, r *http.Request) {
	c, id, ok := h.client(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	err := c.RemoveVolume(r.Context(), name)
	h.audit(r, "volume.remove", id, name, err, nil)
	if h.dockerErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DockerHandler) Networks(w http.ResponseWriter, r *http.Request) {
	c, _, ok := h.client(w, r)
	if !ok {
		return
	}
	list, err := c.ListNetworks(r.Context())
	if h.dockerErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *DockerHandler) RemoveNetwork(w http.ResponseWriter, r *http.Request) {
	c, id, ok := h.client(w, r)
	if !ok {
		return
	}
	nid := r.PathValue("nid")
	err := c.RemoveNetwork(r.Context(), nid)
	h.audit(r, "network.remove", id, nid, err, nil)
	if h.dockerErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
