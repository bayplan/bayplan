package api

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bayplan/bayplan/internal/agent"
	"github.com/bayplan/bayplan/internal/session"
	"github.com/bayplan/bayplan/internal/settings"
)

// Creation and management of clusters with the agent connector (see
// internal/agent).
//
// The flow: the admin creates the cluster in the UI with
// just a name, receives a `kubectl apply -f <URL>` and runs it against the
// cluster. The manifest ships the agent with a key of its own for that cluster;
// when the agent connects, the Hub notifies the Manager and the cluster becomes
// "connected" without anyone having pasted a CA or a token.

// agentOnboarding is what the UI shows after creating an agent cluster (and
// when regenerating the manifest): the temporary YAML URL and the command to run.
type agentOnboarding struct {
	Cluster     clusterResponse `json:"cluster"`
	ManifestURL string          `json:"manifestUrl,omitempty"`
	Command     string          `json:"command"`
	ExpiresAt   time.Time       `json:"expiresAt"`
}

func (h *ClusterHandler) publicURL(r *http.Request) (string, bool) {
	var srv settings.Server
	if h.Settings != nil {
		_ = h.Settings.Get(r.Context(), settings.KeyServer, &srv)
	}
	if srv.PublicURL != "" {
		return strings.TrimRight(srv.PublicURL, "/"), true
	}
	return "", false
}

// createAgentCluster inserts the row with the agent key and returns the
// onboarding. It requires the public URL to be configured: it goes inside the
// manifest and is what the agent will dial from the cluster; without it there
// is nothing to generate.
func (h *ClusterHandler) createAgentCluster(w http.ResponseWriter, r *http.Request, sess *session.Session, in clusterInput) {
	publicURL, ok := h.publicURL(r)
	if !ok {
		http.Error(w, "configure the server's public URL in Settings before creating an agent cluster: it goes inside the manifest", http.StatusBadRequest)
		return
	}
	agentKey, agentHash, err := agent.NewKey()
	if err != nil {
		h.Logger.Error("clusters.createAgent: failed to generate key", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	manifestToken, manifestHash, err := agent.NewKey()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	expires := time.Now().Add(agent.ManifestTokenTTL)
	kind := "kubernetes"
	if in.Kind == "docker" {
		kind = "docker"
	}
	rbacMode := rbacModeScoped
	if in.RBACMode != nil {
		if !validRBACMode(*in.RBACMode) {
			http.Error(w, "rbacMode must be scoped or cluster-admin", http.StatusBadRequest)
			return
		}
		rbacMode = *in.RBACMode
	}
	// The agent key is stored encrypted in addition to the hash: it is needed
	// in the clear to render the manifest (now and when regenerating it), and
	// the hash to authenticate without decrypting on every handshake. Both go
	// in the same INSERT: a second UPDATE could fail and leave an agent
	// cluster whose manifest can never be rendered.
	keyID, enc, err := h.Keyring.Encrypt([]byte(agentKey))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var id int64
	err = h.DB.QueryRow(r.Context(),
		`INSERT INTO clusters (display_name, description, kind, connector, api_endpoint, agent_key_hash, manifest_token_hash, manifest_token_expires_at, insecure_skip_tls_verify, rbac_mode, sa_token_encrypted, key_id)
		 VALUES ($1, NULLIF($2, ''), $7, 'agent', 'agent', $3, $4, $5, $6, $8, $9, $10) RETURNING id`,
		*in.DisplayName, deref(in.Description), agentHash, manifestHash, expires, in.InsecureSkipTLSVerify != nil && *in.InsecureSkipTLSVerify, kind, rbacMode, enc, keyID,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			http.Error(w, "a cluster with that name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("clusters.createAgent: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.auditCluster(r, "cluster.create", *in.DisplayName, map[string]any{"connector": "agent", "kind": kind, "rbacMode": rbacMode})
	row, err := scanClusterRow(h.DB.QueryRow(r.Context(), `SELECT `+clusterListColumns+clusterListFrom+` WHERE c.id = $1`, id))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, h.onboarding(r, sess, row, publicURL, manifestToken, agentKey, expires))
}

// agentCACert is the CA the agents are told to trust (base64 PEM): the one
// pasted in Settings wins (an ingress with its own CA in front), otherwise the
// server's own self-signed CA. Empty = system CAs only.
func (h *ClusterHandler) agentCACert(srv settings.Server) string {
	pemData := []byte(strings.TrimSpace(srv.AgentCACert))
	if len(pemData) == 0 && h.ServerCA != nil {
		pemData = h.ServerCA()
	}
	if len(pemData) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(pemData)
}

// dockerRunCommand is the manifest's equivalent for a Docker host: the agent
// runs as a container with the socket mounted. The key goes in the command
// itself: there is no API server to leave it in.
func (h *ClusterHandler) dockerRunCommand(r *http.Request, id int64, publicURL, agentKey string, insecure bool) string {
	var srv settings.Server
	if h.Settings != nil {
		_ = h.Settings.Get(r.Context(), settings.KeyServer, &srv)
	}
	ins := ""
	if insecure {
		ins = " \\\n  -e BAYPLAN_INSECURE_SKIP_VERIFY=true"
	} else if ca := h.agentCACert(srv); ca != "" {
		ins = " \\\n  -e BAYPLAN_CA_CERT=" + ca
	}
	// --user 0: the image runs as UID 10001 (the kubelet needs a numeric
	// non-root user), which cannot open a root:docker 0660 socket. Root
	// inside this container grants nothing the mounted socket does not
	// already grant (it is root on the host either way).
	return "docker run -d --name bayplan-agent --restart=always --user 0 \\\n" +
		"  -v /var/run/docker.sock:/var/run/docker.sock \\\n" +
		"  -e BAYPLAN_MODE=docker \\\n" +
		"  -e BAYPLAN_SERVER_URL=" + publicURL + " \\\n" +
		"  -e BAYPLAN_CLUSTER_ID=" + strconv.FormatInt(id, 10) + " \\\n" +
		"  -e BAYPLAN_AGENT_KEY=" + agentKey + ins + " \\\n" +
		"  " + srv.EffectiveAgentImage()
}

func (h *ClusterHandler) onboarding(r *http.Request, sess *session.Session, row clusterRow, publicURL, manifestToken, agentKey string, expires time.Time) agentOnboarding {
	var uid int64
	if sess != nil {
		uid = sess.UserID
	}
	if row.kind == "docker" {
		return agentOnboarding{
			Cluster:   h.toResponse(r.Context(), uid, row),
			Command:   h.dockerRunCommand(r, row.id, publicURL, agentKey, row.insecureSkipTLSVerify),
			ExpiresAt: expires,
		}
	}
	url := publicURL + "/api/agent/manifest/" + strconv.FormatInt(row.id, 10) + "/" + manifestToken + ".yaml"
	var srv settings.Server
	if h.Settings != nil {
		_ = h.Settings.Get(r.Context(), settings.KeyServer, &srv)
	}
	return agentOnboarding{
		Cluster:     h.toResponse(r.Context(), uid, row),
		ManifestURL: url,
		Command:     manifestCommand(url, h.agentCACert(srv)),
		ExpiresAt:   expires,
	}
}

// manifestCommand is what the operator runs against the cluster. With a
// private CA (the built-in self-signed one, or one pasted in Settings)
// `kubectl apply -f <url>` fails verifying the server, and the only fix
// people find is skipping verification for a download that carries the
// agent key. The command verifies against that CA instead, and falls back
// to the system roots for when a proxy with a public certificate sits in
// front (the CA is still offered, but it is not the one serving the URL).
func manifestCommand(url, caB64 string) string {
	if caB64 == "" {
		return "kubectl apply -f " + url
	}
	return "{ curl -fsSL --cacert <(echo '" + caB64 + "' | base64 -d) '" + url + "' || curl -fsSL '" + url + "'; } | kubectl apply -f -"
}

// RegenerateManifest issues a new token for the manifest URL (the previous one
// stops working). The agent key does NOT change: an already deployed agent
// keeps connecting; this is for reinstalling it or for when the URL expired
// unused.
func (h *ClusterHandler) RegenerateManifest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	publicURL, ok := h.publicURL(r)
	if !ok {
		http.Error(w, "configure the server's public URL in Settings", http.StatusBadRequest)
		return
	}
	manifestToken, manifestHash, err := agent.NewKey()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	expires := time.Now().Add(agent.ManifestTokenTTL)
	tag, err := h.DB.Exec(r.Context(),
		`UPDATE clusters SET manifest_token_hash = $2, manifest_token_expires_at = $3, updated_at = now() WHERE id = $1 AND connector = 'agent'`,
		id, manifestHash, expires)
	if err != nil {
		h.Logger.Error("clusters.regenerateManifest", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "agent cluster not found", http.StatusNotFound)
		return
	}
	row, err := scanClusterRow(h.DB.QueryRow(r.Context(), `SELECT `+clusterListColumns+clusterListFrom+` WHERE c.id = $1`, id))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	agentKey := ""
	if row.kind == "docker" {
		var enc []byte
		var keyID *string
		if err := h.DB.QueryRow(r.Context(), `SELECT sa_token_encrypted, key_id FROM clusters WHERE id = $1`, id).Scan(&enc, &keyID); err == nil {
			if k, err := h.Keyring.Decrypt(keyIDFrom(keyID), enc); err == nil {
				agentKey = string(k)
			}
		}
	}
	sess, _ := sessionFrom(r)
	h.auditCluster(r, "cluster.agent_manifest", row.displayName, nil)
	writeJSON(w, http.StatusOK, h.onboarding(r, sess, row, publicURL, manifestToken, agentKey, expires))
}

// AgentManifest serves GET /api/agent/manifest/{id}/{token}.yaml WITHOUT a
// session: kubectl downloads it from the operator's machine. It authenticates
// with the manifest token (hash in DB, 1 h, reusable within the window).
func (h *ClusterHandler) AgentManifest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	token := strings.TrimSuffix(r.PathValue("token"), ".yaml")
	var hash *string
	var expires *time.Time
	var enc []byte
	var keyID *string
	var insecure bool
	var rbacMode string
	err = h.DB.QueryRow(r.Context(),
		`SELECT manifest_token_hash, manifest_token_expires_at, sa_token_encrypted, key_id, insecure_skip_tls_verify, rbac_mode FROM clusters WHERE id = $1 AND connector = 'agent' AND enabled`, id,
	).Scan(&hash, &expires, &enc, &keyID, &insecure, &rbacMode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// Checked first: a DB failure is not "not found", and hiding it as a
		// 404 would send the operator to regenerate a perfectly valid URL.
		h.Logger.Error("agent.manifest", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err != nil || hash == nil || expires == nil || time.Now().After(*expires) || subtle.ConstantTimeCompare([]byte(agent.HashKey(token)), []byte(*hash)) != 1 {
		// Same 404 for everything: the cluster's existence is not confirmed.
		http.Error(w, "not found or expired", http.StatusNotFound)
		return
	}
	agentKey, err := h.Keyring.Decrypt(keyIDFrom(keyID), enc)
	if err != nil {
		h.Logger.Error("agent.manifest: failed to decrypt key", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	publicURL, _ := h.publicURL(r)
	var srv settings.Server
	if h.Settings != nil {
		_ = h.Settings.Get(r.Context(), settings.KeyServer, &srv)
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(agent.RenderManifest(agent.ManifestParams{
		ServerURL: publicURL, ClusterID: id, AgentKey: string(agentKey), Image: srv.EffectiveAgentImage(), Insecure: insecure, CACert: h.agentCACert(srv), ClusterAdmin: rbacMode == rbacModeClusterAdmin,
	})))
}

// AgentRBAC serves GET /api/agent/rbac to the agent itself (agent credential,
// no session): the ClusterRoles and ClusterRoleBindings currently in force for
// its cluster according to its rbac_mode. What the agent applies with its SA.
func (h *ClusterHandler) AgentRBAC(w http.ResponseWriter, r *http.Request) {
	if h.AgentAuth == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id, err := h.AgentAuth.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var kind string
	if err := h.DB.QueryRow(r.Context(), `SELECT kind FROM clusters WHERE id = $1`, id).Scan(&kind); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if kind != "kubernetes" {
		http.Error(w, "this environment has no Kubernetes RBAC", http.StatusNotFound)
		return
	}
	y := agent.RenderRBACOnly()
	var srv settings.Server
	_ = h.Settings.Get(r.Context(), settings.KeyServer, &srv)
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("X-Bayplan-RBAC-Hash", agent.RBACHash(y))
	// Image the server expects from the agent: if it differs from the one
	// running, the agent upgrades itself (same repository, another tag).
	w.Header().Set(agent.HeaderAgentImage, srv.EffectiveAgentImage())
	_, _ = w.Write([]byte(y))
}

// AgentRBACStatus receives POST /api/agent/rbac/status {hash, error} after
// every apply attempt. It is only stored: the UI compares the hash with the
// expected one.
func (h *ClusterHandler) AgentRBACStatus(w http.ResponseWriter, r *http.Request) {
	if h.AgentAuth == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id, err := h.AgentAuth.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		Hash  string `json:"hash"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&in); err != nil || len(in.Hash) > 64 || len(in.Error) > 2000 {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if _, err := h.DB.Exec(r.Context(),
		`UPDATE clusters SET agent_rbac_hash = NULLIF($2, ''), agent_rbac_applied_at = now(), agent_rbac_error = NULLIF($3, '') WHERE id = $1`,
		id, in.Hash, in.Error); err != nil {
		h.Logger.Error("agent.rbac.status: save failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if in.Error != "" {
		h.Logger.Warn("agent: failed to apply its RBAC", "cluster", id, "error", in.Error)
	}
	w.WriteHeader(http.StatusNoContent)
}
