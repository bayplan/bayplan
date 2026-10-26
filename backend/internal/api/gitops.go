package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/gitops"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/rbac"
)

// GitOpsHandler manages a cluster's GitOps sources. Creating, pausing and
// syncing is editor level (it is deploying); deleting is admin, like the rest.
type GitOpsHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	K8s    *k8s.Manager
	Rec    *gitops.Reconciler
}

var scpLikeRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

var sourceNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (h *GitOpsHandler) audit(r *http.Request, action string, clusterID int64, name string, err error, detail map[string]any) {
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
		Cluster: h.K8s.DisplayName(clusterID), Resource: name, Result: result, Detail: detail,
	})
}

func (h *GitOpsHandler) List(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	list, err := h.Rec.List(r.Context(), id)
	if err != nil {
		h.Logger.Error("gitops.list", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type createSourceResponse struct {
	gitops.Source
	// WebhookToken is only returned on creation: afterwards only its hash exists.
	WebhookToken string `json:"webhookToken"`
	WebhookPath  string `json:"webhookPath"`
}

func (h *GitOpsHandler) Create(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	sess, _ := middleware.SessionFromContext(r.Context())
	var body struct {
		Name            string `json:"name"`
		RepoURL         string `json:"repoUrl"`
		Branch          string `json:"branch"`
		Path            string `json:"path"`
		Namespace       string `json:"namespace"`
		Username        string `json:"username"`
		Token           string `json:"token"`
		IntervalSeconds int    `json:"intervalSeconds"`
		AuthType        string `json:"authType"`
		SSHKnownHosts   string `json:"sshKnownHosts"`
		Prune           bool   `json:"prune"`
		Kustomize       bool   `json:"kustomize"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.RepoURL = strings.TrimSpace(body.RepoURL)
	body.Branch = strings.TrimSpace(body.Branch)
	body.Path = strings.Trim(strings.TrimSpace(body.Path), "/")
	if body.Branch == "" {
		body.Branch = "main"
	}
	if body.Path == "" {
		body.Path = "."
	}
	if body.IntervalSeconds == 0 {
		body.IntervalSeconds = 300
	}
	if !sourceNameRe.MatchString(body.Name) {
		http.Error(w, "name must be a lowercase identifier (letters, digits and hyphens)", http.StatusBadRequest)
		return
	}
	if body.AuthType == "" {
		body.AuthType = gitops.AuthHTTPS
	}
	switch body.AuthType {
	case gitops.AuthHTTPS:
		u, err := url.Parse(body.RepoURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			http.Error(w, "repoUrl must be an http(s) URL; for SSH choose that authentication type", http.StatusBadRequest)
			return
		}
		// The URL is shown to every reader of the cluster and goes to the
		// audit log; a token in it would leak to both. It goes in `token`.
		if u.User != nil {
			http.Error(w, "do not put credentials in repoUrl: use the token field", http.StatusBadRequest)
			return
		}
	case gitops.AuthSSH:
		// ssh://git@host/org/repo.git or the scp form git@host:org/repo.git.
		if u, err := url.Parse(body.RepoURL); !(err == nil && u.Scheme == "ssh" && u.Host != "") && !scpLikeRe.MatchString(body.RepoURL) {
			http.Error(w, "SSH repoUrl must be ssh://user@host/path or user@host:path", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Token) == "" || !strings.Contains(body.Token, "PRIVATE KEY") {
			http.Error(w, "an SSH source needs the private key (PEM, without passphrase) in token", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.SSHKnownHosts) == "" {
			http.Error(w, "an SSH source needs sshKnownHosts (the output of ssh-keyscan <host>) to verify the server", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "authType must be https or ssh", http.StatusBadRequest)
		return
	}
	// Prune deletes with the admins group (editors have no delete), so only an
	// admin on the cluster may enable it: otherwise an editor could delete via
	// GitOps what they cannot delete by hand.
	if body.Prune {
		if role, ok := middleware.RoleFromContext(r.Context()); !ok || !role.AtLeast(rbac.RoleAdmin) {
			http.Error(w, "enabling prune requires being admin on the cluster", http.StatusForbidden)
			return
		}
	}
	// An editor picks the repo URL and the server clones it: the link-local
	// range (cloud metadata) is rejected here and when connecting.
	if err := gitops.RejectLinkLocal(body.RepoURL); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.IntervalSeconds < 30 || body.IntervalSeconds > 86400 {
		http.Error(w, "intervalSeconds must be between 30 and 86400", http.StatusBadRequest)
		return
	}
	if strings.Contains(body.Path, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	var username string
	if sess != nil {
		username = sess.Username
	}
	src, token, err := h.Rec.Create(r.Context(), gitops.CreateInput{
		ClusterID: id, Name: body.Name, RepoURL: body.RepoURL, Branch: body.Branch, Path: body.Path, Namespace: strings.TrimSpace(body.Namespace),
		Username: strings.TrimSpace(body.Username), Token: body.Token, IntervalSeconds: body.IntervalSeconds, CreatedBy: username,
		AuthType: body.AuthType, SSHKnownHosts: body.SSHKnownHosts, Prune: body.Prune, Kustomize: body.Kustomize,
	})
	if err != nil {
		if isUniqueViolation(err) {
			http.Error(w, "a source with that name already exists in this cluster", http.StatusConflict)
			return
		}
		h.Logger.Error("gitops.create", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "gitops.create", id, src.Name, nil, map[string]any{"repo": src.RepoURL, "branch": src.Branch, "path": src.Path, "authType": src.AuthType, "prune": src.Prune, "kustomize": src.Kustomize})
	writeJSON(w, http.StatusCreated, createSourceResponse{Source: src, WebhookToken: token, WebhookPath: "/api/gitops/webhook/" + strconv.FormatInt(src.ID, 10)})
}

// sourceFor checks that the source belongs to the cluster in the route: the
// middleware authorises per cluster, so a source id from another cluster cannot
// slip through here.
func (h *GitOpsHandler) sourceFor(w http.ResponseWriter, r *http.Request) (gitops.Source, bool) {
	clusterID, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return gitops.Source{}, false
	}
	sid, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid source id", http.StatusBadRequest)
		return gitops.Source{}, false
	}
	src, err := h.Rec.Get(r.Context(), sid)
	if errors.Is(err, gitops.ErrSourceNotFound) || (err == nil && src.ClusterID != clusterID) {
		http.Error(w, "source not found", http.StatusNotFound)
		return gitops.Source{}, false
	}
	if err != nil {
		h.Logger.Error("gitops.get", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return gitops.Source{}, false
	}
	return src, true
}

func (h *GitOpsHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	src, ok := h.sourceFor(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	out, err := h.Rec.SetEnabled(r.Context(), src.ID, body.Enabled)
	if err != nil {
		h.Logger.Error("gitops.setEnabled", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	action := "gitops.resume"
	if !body.Enabled {
		action = "gitops.pause"
	}
	h.audit(r, action, src.ClusterID, src.Name, nil, nil)
	writeJSON(w, http.StatusOK, out)
}

func (h *GitOpsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	src, ok := h.sourceFor(w, r)
	if !ok {
		return
	}
	if _, err := h.Rec.Delete(r.Context(), src.ID); err != nil {
		h.Logger.Error("gitops.delete", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "gitops.delete", src.ClusterID, src.Name, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

// Sync: manual, synchronous sync (cloning a small repo takes seconds; the UI
// shows the result directly).
func (h *GitOpsHandler) Sync(w http.ResponseWriter, r *http.Request) {
	src, ok := h.sourceFor(w, r)
	if !ok {
		return
	}
	res, err := h.Rec.Sync(r.Context(), src.ID, "manual")
	h.audit(r, "gitops.sync", src.ClusterID, src.Name, err, map[string]any{"commit": res.Commit, "objects": len(res.Applied)})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "commit": res.Commit, "applied": res.Applied})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// Webhook: POST /api/gitops/webhook/{sid}. No session: it authenticates with the
// source's token in a header (X-Webhook-Token / X-Gitlab-Token) or with the HMAC
// signature of the body (X-Hub-Signature-256, GitHub/Gitea/Bitbucket) using the
// token as secret. Never via query string: it would end up in the access logs.
// It answers 202 and syncs in the background: GitHub/GitLab cut the webhook off
// at 10 s and a clone can take longer.
func (h *GitOpsHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	sid, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	token := r.Header.Get("X-Webhook-Token")
	if token == "" {
		token = r.Header.Get("X-Gitlab-Token")
	}
	ok, err := h.Rec.VerifyWebhook(r.Context(), sid, token, r.Header.Get("X-Hub-Signature-256"), body)
	if errors.Is(err, gitops.ErrSourceNotFound) || (err == nil && !ok) {
		// Same 404 for "does not exist" and "bad token": the source's existence
		// is not confirmed to whoever lacks the token.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("gitops.webhook", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	go func() {
		_, _ = h.Rec.Sync(context.WithoutCancel(r.Context()), sid, "webhook")
	}()
	w.WriteHeader(http.StatusAccepted)
}
