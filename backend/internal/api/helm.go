package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/helm"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
)

// HelmHandler exposes chart repositories (platform, global admin only), the
// catalogue (read with a session) and the operations on a cluster's releases
// (editor to install/upgrade/rollback, admin to uninstall), always with the
// user's impersonated identity, like every other write.
type HelmHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager
	Repos  *helm.Repos
	Client *helm.Client
}

type helmRepoResponse struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	Type            string    `json:"type"` // http | oci
	Username        string    `json:"username,omitempty"`
	HasPassword     bool      `json:"hasPassword"`
	InsecureSkipTLS bool      `json:"insecureSkipTLS"`
	CreatedAt       time.Time `json:"createdAt"`
}

func toRepoResponse(rp helm.Repo) helmRepoResponse {
	t := "http"
	if rp.IsOCI() {
		t = "oci"
	}
	return helmRepoResponse{ID: rp.ID, Name: rp.Name, URL: rp.URL, Type: t, Username: rp.Username, HasPassword: len(rp.PasswordEncrypted) > 0, InsecureSkipTLS: rp.InsecureSkipTLS, CreatedAt: rp.CreatedAt}
}

func (h *HelmHandler) ListRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := h.Repos.List(r.Context())
	if err != nil {
		h.Logger.Error("helm.repos.list", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Every user lists the repositories to pick a chart; the account used
	// against a private one is the platform admin's business.
	role, _ := middleware.RoleFromContext(r.Context())
	out := make([]helmRepoResponse, 0, len(repos))
	for _, rp := range repos {
		resp := toRepoResponse(rp)
		if !role.IsAdmin() {
			resp.Username = ""
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

var repoNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CreateRepo validates and registers a repository. For HTTP repos it downloads
// the index BEFORE saving: a mistyped URL is rejected on the spot instead of
// showing up empty in the catalogue. For OCI there is no index to check.
func (h *HelmHandler) CreateRepo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string `json:"name"`
		URL             string `json:"url"`
		Username        string `json:"username"`
		Password        string `json:"password"`
		InsecureSkipTLS bool   `json:"insecureSkipTLS"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.URL = strings.TrimRight(strings.TrimSpace(body.URL), "/")
	body.Username = strings.TrimSpace(body.Username)
	if !repoNameRe.MatchString(body.Name) {
		http.Error(w, "name must be a lowercase identifier (letters, digits and hyphens)", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(body.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "oci") || u.Host == "" {
		http.Error(w, "url must be http(s):// or oci://", http.StatusBadRequest)
		return
	}
	// The URL is listed to every user and written to the audit log: a password
	// in it would leak to both. The credentials go in their own fields.
	if u.User != nil {
		http.Error(w, "do not put credentials in the url: use the username and password fields", http.StatusBadRequest)
		return
	}

	probe := helm.Repo{Name: body.Name, URL: body.URL, Username: body.Username, InsecureSkipTLS: body.InsecureSkipTLS}
	if !probe.IsOCI() {
		// Dry check with the credentials in the clear, before saving.
		if err := h.Repos.Probe(r.Context(), probe, body.Password); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}

	rp, err := h.Repos.Create(r.Context(), body.Name, body.URL, body.Username, body.Password, body.InsecureSkipTLS)
	if err != nil {
		if isUniqueViolation(err) {
			http.Error(w, "a repository with that name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("helm.repos.create", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.auditRepo(r, "helmrepo.create", rp)
	writeJSON(w, http.StatusCreated, toRepoResponse(rp))
}

func (h *HelmHandler) DeleteRepo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	rp, err := h.Repos.Delete(r.Context(), id)
	if errors.Is(err, helm.ErrRepoNotFound) {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("helm.repos.delete", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.auditRepo(r, "helmrepo.delete", rp)
	w.WriteHeader(http.StatusNoContent)
}

func (h *HelmHandler) auditRepo(r *http.Request, action string, rp helm.Repo) {
	sess, _ := middleware.SessionFromContext(r.Context())
	var uid *int64
	var uname string
	if sess != nil {
		uid, uname = &sess.UserID, sess.Username
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: uid, ActorUsername: uname, IP: middleware.ClientIP(r), Action: action, Resource: rp.Name,
		Result: audit.ResultSuccess, Detail: map[string]any{"url": rp.URL},
	})
}

func (h *HelmHandler) repoFromPath(w http.ResponseWriter, r *http.Request) (helm.Repo, bool) {
	id, err := strconv.ParseInt(r.PathValue("repoId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid repository id", http.StatusBadRequest)
		return helm.Repo{}, false
	}
	rp, err := h.Repos.Get(r.Context(), id)
	if errors.Is(err, helm.ErrRepoNotFound) {
		http.Error(w, "repository not found", http.StatusNotFound)
		return helm.Repo{}, false
	}
	if err != nil {
		h.Logger.Error("helm.repo.get", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return helm.Repo{}, false
	}
	return rp, true
}

// SearchCharts: GET /api/helm/repos/{repoId}/charts?q=&refresh=1.
// OCI has no index: ?chart= is required and its tags are returned.
func (h *HelmHandler) SearchCharts(w http.ResponseWriter, r *http.Request) {
	rp, ok := h.repoFromPath(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if rp.IsOCI() {
		chart := strings.TrimSpace(q.Get("chart"))
		if chart == "" {
			writeJSON(w, http.StatusOK, []helm.ChartEntry{})
			return
		}
		tags, err := h.Repos.OCITags(r.Context(), rp, chart)
		if errors.Is(err, helm.ErrInvalidInput) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if len(tags) == 0 {
			writeJSON(w, http.StatusOK, []helm.ChartEntry{})
			return
		}
		writeJSON(w, http.StatusOK, []helm.ChartEntry{{Name: chart, Version: tags[0], Versions: tags}})
		return
	}
	entries, err := h.Repos.Search(r.Context(), rp, q.Get("q"), q.Get("refresh") == "1")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// ChartInfo: GET /api/helm/repos/{repoId}/charts/{chart}?version=.
func (h *HelmHandler) ChartInfo(w http.ResponseWriter, r *http.Request) {
	rp, ok := h.repoFromPath(w, r)
	if !ok {
		return
	}
	ref := helm.ChartRef{Repo: rp, Chart: r.PathValue("chart"), Version: r.URL.Query().Get("version")}
	info, err := h.Client.Chart(ref)
	if errors.Is(err, helm.ErrInvalidInput) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// --- Releases ---

type helmActor struct {
	actor
	clusterID int64
}

func (h *HelmHandler) resolve(w http.ResponseWriter, r *http.Request) (helmActor, bool) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return helmActor{}, false
	}
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return helmActor{}, false
	}
	// The role on THIS cluster, already capped for read-only tokens: the one
	// RequireClusterRole left in the context (every release route has it).
	role, ok := middleware.RoleFromContext(r.Context())
	if !ok {
		http.Error(w, "no permissions on this cluster", http.StatusForbidden)
		return helmActor{}, false
	}
	return helmActor{actor: actor{userID: sess.UserID, username: sess.Username, group: k8s.GroupForRole(role)}, clusterID: id}, true
}

func (h *HelmHandler) helmErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, helm.ErrReleaseNotFound):
		http.Error(w, "release not found", http.StatusNotFound)
	case errors.Is(err, helm.ErrInvalidInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, k8s.ErrClusterNotFound):
		http.Error(w, "cluster not found or not connected", http.StatusNotFound)
	default:
		// Helm or API server error: the text is the real explanation.
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
	return true
}

func (h *HelmHandler) audit(r *http.Request, a helmActor, action, namespace, name string, err error, detail map[string]any) {
	result := audit.ResultSuccess
	if err != nil {
		result = audit.ResultFailure
		if detail == nil {
			detail = map[string]any{}
		}
		detail["error"] = err.Error()
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &a.userID, ActorUsername: a.username, IP: middleware.ClientIP(r), Action: action,
		Cluster: h.K8s.DisplayName(a.clusterID), Namespace: namespace, Resource: name, Result: result, Detail: detail,
	})
}

// Reads (List/Get) go with the technical ServiceAccount, like the rest of the
// panel's reads: a readonly user has to be able to see what is installed without
// their impersonated group needing permission to list Secrets in the cluster.
func (h *HelmHandler) ListReleases(w http.ResponseWriter, r *http.Request) {
	a, ok := h.resolve(w, r)
	if !ok {
		return
	}
	cfg, err := h.K8s.BaseRestConfig(a.clusterID)
	if h.helmErr(w, err) {
		return
	}
	list, err := h.Client.List(r.Context(), cfg, parseNamespaces(r))
	if h.helmErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *HelmHandler) GetRelease(w http.ResponseWriter, r *http.Request) {
	a, ok := h.resolve(w, r)
	if !ok {
		return
	}
	cfg, err := h.K8s.BaseRestConfig(a.clusterID)
	if h.helmErr(w, err) {
		return
	}
	d, err := h.Client.Get(r.Context(), cfg, r.PathValue("namespace"), r.PathValue("name"))
	if h.helmErr(w, err) {
		return
	}
	// The read goes with the technical SA, so the manifest's Secrets are masked
	// by the panel with the same rule as a Secret's YAML: only an admin (not
	// capped by a read-only token) sees the values, and it is audited as
	// secret.read.
	// The panel's own release (chart in the local cluster): its manifest and
	// its values carry the encryption key and the DB; nobody sees them from here.
	if p := h.K8s.ProtectedNamespace(a.clusterID); p != "" && r.PathValue("namespace") == p {
		d.Manifest, _ = helm.RedactSecrets(d.Manifest)
		d.Values = "# values of the panel itself: not shown"
	} else if a.group != k8s.GroupAdmins {
		// The values are where the passwords the chart writes into those
		// Secrets come from: same rule as the Secrets themselves.
		d.Manifest, _ = helm.RedactSecrets(d.Manifest)
		d.Values = "# values are only shown to cluster admins"
	} else if _, n := helm.RedactSecrets(d.Manifest); n > 0 {
		audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
			UserID: &a.userID, ActorUsername: a.username, IP: middleware.ClientIP(r), Action: "secret.read",
			Cluster: h.K8s.DisplayName(a.clusterID), Namespace: r.PathValue("namespace"), Resource: "helm/" + r.PathValue("name"),
			Result: audit.ResultSuccess, Detail: map[string]any{"secrets": n, "via": "helm.release"},
		})
	}
	writeJSON(w, http.StatusOK, d)
}

type releaseInput struct {
	RepoID          int64  `json:"repoId"`
	Chart           string `json:"chart"`
	Version         string `json:"version"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Values          string `json:"values"`
	CreateNamespace bool   `json:"createNamespace"`
	DryRun          bool   `json:"dryRun"`
}

var releaseNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,51}[a-z0-9])?$`)

func (h *HelmHandler) readReleaseInput(w http.ResponseWriter, r *http.Request) (releaseInput, helm.Repo, bool) {
	var in releaseInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return in, helm.Repo{}, false
	}
	in.Name, in.Namespace, in.Chart, in.Version = strings.TrimSpace(in.Name), strings.TrimSpace(in.Namespace), strings.TrimSpace(in.Chart), strings.TrimSpace(in.Version)
	if !releaseNameRe.MatchString(in.Name) || in.Namespace == "" || in.Chart == "" {
		http.Error(w, "name (lowercase, digits and hyphens), namespace and chart are required", http.StatusBadRequest)
		return in, helm.Repo{}, false
	}
	rp, err := h.Repos.Get(r.Context(), in.RepoID)
	if errors.Is(err, helm.ErrRepoNotFound) {
		http.Error(w, "repository not found", http.StatusBadRequest)
		return in, helm.Repo{}, false
	}
	if err != nil {
		h.Logger.Error("helm.repo.get", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return in, helm.Repo{}, false
	}
	return in, rp, true
}

type releaseResult struct {
	Release  helm.ReleaseSummary `json:"release"`
	Manifest string              `json:"manifest,omitempty"`
	DryRun   bool                `json:"dryRun"`
}

// Install: POST /api/clusters/{id}/helm/releases.
func (h *HelmHandler) Install(w http.ResponseWriter, r *http.Request) {
	a, ok := h.resolve(w, r)
	if !ok {
		return
	}
	in, rp, ok := h.readReleaseInput(w, r)
	if !ok {
		return
	}
	cfg, err := h.K8s.ImpersonatedRestConfig(a.clusterID, a.username, a.group)
	if h.helmErr(w, err) {
		return
	}
	rel, manifest, err := h.Client.Install(r.Context(), cfg, helm.ChartRef{Repo: rp, Chart: in.Chart, Version: in.Version},
		helm.InstallOptions{Name: in.Name, Namespace: in.Namespace, Values: in.Values, CreateNamespace: in.CreateNamespace, DryRun: in.DryRun})
	if !in.DryRun || err != nil {
		h.audit(r, a, "helm.install", in.Namespace, in.Name, err, map[string]any{"repo": rp.Name, "chart": in.Chart, "version": in.Version, "dryRun": in.DryRun})
	}
	if h.helmErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, releaseResult{Release: rel, Manifest: h.manifestIf(a, in.DryRun, manifest), DryRun: in.DryRun})
}

// Upgrade: PUT /api/clusters/{id}/helm/releases/{namespace}/{name}.
func (h *HelmHandler) Upgrade(w http.ResponseWriter, r *http.Request) {
	a, ok := h.resolve(w, r)
	if !ok {
		return
	}
	in, rp, ok := h.readReleaseInput(w, r)
	if !ok {
		return
	}
	// The release is identified by the route, not the body.
	in.Namespace, in.Name = r.PathValue("namespace"), r.PathValue("name")
	cfg, err := h.K8s.ImpersonatedRestConfig(a.clusterID, a.username, a.group)
	if h.helmErr(w, err) {
		return
	}
	rel, manifest, err := h.Client.Upgrade(r.Context(), cfg, helm.ChartRef{Repo: rp, Chart: in.Chart, Version: in.Version},
		helm.InstallOptions{Name: in.Name, Namespace: in.Namespace, Values: in.Values, DryRun: in.DryRun})
	if !in.DryRun || err != nil {
		h.audit(r, a, "helm.upgrade", in.Namespace, in.Name, err, map[string]any{"repo": rp.Name, "chart": in.Chart, "version": in.Version, "dryRun": in.DryRun})
	}
	if h.helmErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, releaseResult{Release: rel, Manifest: h.manifestIf(a, in.DryRun, manifest), DryRun: in.DryRun})
}

// manifestIf returns the rendered manifest of a dry-run, masked for anyone
// below admin like GetRelease does: a chart may `lookup` existing Secrets with
// the user's identity (an editor can read them in the cluster, the panel does
// not show them), and the server dry-run renders what it found.
func (h *HelmHandler) manifestIf(a helmActor, dryRun bool, m string) string {
	if dryRun {
		if a.group != k8s.GroupAdmins {
			m, _ = helm.RedactSecrets(m)
		}
		return m
	}
	return ""
}

// Rollback: POST .../{namespace}/{name}/rollback {revision}.
func (h *HelmHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	a, ok := h.resolve(w, r)
	if !ok {
		return
	}
	var body struct {
		Revision int `json:"revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Revision < 0 {
		http.Error(w, "invalid revision", http.StatusBadRequest)
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	cfg, err := h.K8s.ImpersonatedRestConfig(a.clusterID, a.username, a.group)
	if h.helmErr(w, err) {
		return
	}
	err = h.Client.Rollback(r.Context(), cfg, ns, name, body.Revision)
	h.audit(r, a, "helm.rollback", ns, name, err, map[string]any{"revision": body.Revision})
	if h.helmErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Uninstall: DELETE .../{namespace}/{name}. Admin only (router).
func (h *HelmHandler) Uninstall(w http.ResponseWriter, r *http.Request) {
	a, ok := h.resolve(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	cfg, err := h.K8s.ImpersonatedRestConfig(a.clusterID, a.username, a.group)
	if h.helmErr(w, err) {
		return
	}
	err = h.Client.Uninstall(r.Context(), cfg, ns, name)
	h.audit(r, a, "helm.uninstall", ns, name, err, nil)
	if h.helmErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
