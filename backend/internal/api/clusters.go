package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/agent"
	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/cloud"
	"github.com/bayplan/bayplan/internal/crypto"
	"github.com/bayplan/bayplan/internal/dockerenv"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/bayplan/bayplan/internal/session"
	"github.com/bayplan/bayplan/internal/settings"
)

// ClusterHandler manages cluster creation/editing/removal. Credentials are
// validated against the real cluster before being persisted (immediate feedback
// to the admin) and are stored encrypted (AES-GCM, see internal/crypto) — never
// in the clear, and never returned in any response.
type ClusterHandler struct {
	DB       *pgxpool.Pool
	Logger   *slog.Logger
	Keyring  *crypto.Keyring
	K8s      *k8s.Manager
	Settings *settings.Store    // public URL and agent image for the manifest
	Docker   *dockerenv.Manager // status of Docker hosts (kind=docker)
	Cloud    *cloud.Service     // cloud connector (EKS/AKS/GKE); nil disables it
	// AgentAuth authenticates the agent's HTTP requests (RBAC): it is the Hub.
	AgentAuth AgentAuthenticator
	// ServerCA is the self-signed CA of the server's own HTTPS, if any.
	ServerCA func() []byte
}

// AgentAuthenticator validates an agent's credential in an HTTP request
// (cluster header + Bearer) and returns its cluster.
type AgentAuthenticator interface {
	Authenticate(r *http.Request) (int64, error)
	// Evict drops the cluster's live tunnel (delete, disable).
	Evict(clusterID int64)
}

func sessionFrom(r *http.Request) (*session.Session, bool) {
	return middleware.SessionFromContext(r.Context())
}

func keyIDFrom(p *string) crypto.KeyID {
	if p == nil {
		return ""
	}
	return crypto.KeyID(*p)
}

type clusterResponse struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	APIEndpoint string `json:"apiEndpoint"`
	Enabled     bool   `json:"enabled"`
	// AuthType is "token" or "clientCert": the UI shows it so the admin knows
	// which credential has to be rotated.
	AuthType              string `json:"authType"`
	InsecureSkipTLSVerify bool   `json:"insecureSkipTLSVerify"`
	// Kind: "kubernetes" or "docker" (Docker/Podman host via agent).
	Kind string `json:"kind"`
	// Connector: "direct", "agent" (see internal/agent) or "cloud" (internal/cloud).
	Connector string `json:"connector"`
	// Cloud: credential and reference at the provider (connector=cloud only).
	Cloud *clusterCloudInfo `json:"cloud,omitempty"`
	// AgentLastSeen: last time the agent tunnel was open.
	AgentLastSeen *time.Time `json:"agentLastSeen,omitempty"`
	// RBACMode: "scoped" or "cluster-admin" (only meaningful for agent and cloud).
	RBACMode string `json:"rbacMode"`
	// AgentRBAC: status of the RBAC self-applied by the agent (Kubernetes agent only).
	AgentRBAC *agentRBACInfo `json:"agentRbac,omitempty"`
	// Role is the EFFECTIVE role of the requesting user on this specific cluster
	// (global + 'cluster' scope combined, highest privilege wins — see
	// rbac.EffectiveRoleForCluster). The frontend uses this field to gate write
	// actions per cluster, instead of the global role.
	Role string `json:"role"`
	// Status comes from the in-memory Manager: connected, synced, version and
	// last error. The frontend used to cross-reference it by name with /readyz.
	Status k8s.ClusterStatus `json:"status"`
}

// clusterCloudInfo is what the UI shows for a cluster with the cloud connector.
type clusterCloudInfo struct {
	CredentialID int64          `json:"credentialId"`
	Provider     cloud.Provider `json:"provider"`
	Ref          cloud.Ref      `json:"ref"`
}

type clusterRow struct {
	id                    int64
	displayName           string
	description           *string
	apiEndpoint           string
	enabled               bool
	insecureSkipTLSVerify bool
	hasClientKey          bool
	connector             string
	agentLastSeen         *time.Time
	kind                  string
	cloudCredentialID     *int64
	cloudRef              []byte
	cloudProvider         *string
	rbacMode              string
	agentRBACHash         *string
	agentRBACAppliedAt    *time.Time
	agentRBACError        *string
}

// agentRBACInfo: whether the agent has applied the RBAC the panel expects today.
type agentRBACInfo struct {
	UpToDate  bool       `json:"upToDate"`
	AppliedAt *time.Time `json:"appliedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
}

func (h *ClusterHandler) toResponse(ctx context.Context, userID int64, row clusterRow) clusterResponse {
	c := clusterResponse{
		ID: row.id, DisplayName: row.displayName, APIEndpoint: row.apiEndpoint, Enabled: row.enabled,
		InsecureSkipTLSVerify: row.insecureSkipTLSVerify, AuthType: "token", Connector: row.connector, AgentLastSeen: row.agentLastSeen, Kind: row.kind,
		RBACMode: row.rbacMode,
	}
	if row.connector == k8s.ConnectorInCluster {
		c.AuthType = k8s.ConnectorInCluster
	}
	if row.connector == "agent" {
		c.AuthType = "agent"
		c.APIEndpoint = ""
		if row.kind == "kubernetes" {
			info := &agentRBACInfo{AppliedAt: row.agentRBACAppliedAt}
			if row.agentRBACError != nil {
				info.Error = *row.agentRBACError
			}
			expected := agent.RBACHash(agent.RenderRBACOnly())
			info.UpToDate = row.agentRBACHash != nil && *row.agentRBACHash == expected && info.Error == ""
			c.AgentRBAC = info
		}
	}
	if row.connector == "cloud" && row.cloudCredentialID != nil {
		c.AuthType = "cloud"
		info := &clusterCloudInfo{CredentialID: *row.cloudCredentialID}
		if row.cloudProvider != nil {
			info.Provider = cloud.Provider(*row.cloudProvider)
		}
		_ = json.Unmarshal(row.cloudRef, &info.Ref)
		c.Cloud = info
	}
	if row.description != nil {
		c.Description = *row.description
	}
	if row.hasClientKey {
		c.AuthType = "clientCert"
	}
	// Effective role per cluster (global + 'cluster' scope combined): a
	// 'cluster'-scoped admin without a global role would be indistinguishable
	// from a global readonly if the frontend only looked at the session's
	// global role.
	effRole, err := rbac.EffectiveRoleForCluster(ctx, h.DB, userID, row.id)
	effRole = middleware.CapRoleFromContext(ctx, effRole)
	if err != nil && !errors.Is(err, rbac.ErrNoRole) {
		h.Logger.Error("clusters: failed to resolve effective role", "error", err, "clusterId", row.id)
	}
	c.Role = string(effRole)
	if row.kind == "docker" && h.Docker != nil {
		c.Status = h.Docker.Status(row.id)
	} else {
		c.Status = h.K8s.ClusterStatus(row.id)
	}
	return c
}

// clusterListColumns is qualified with the alias `c` because the cloud
// credential's provider comes from a LEFT JOIN (a subquery per row would be the
// same but less readable in the plan).
const clusterListColumns = `c.id, c.display_name, c.description, c.api_endpoint, c.enabled, c.insecure_skip_tls_verify,
	c.client_key_encrypted IS NOT NULL, c.connector, c.agent_last_seen, c.kind, c.cloud_credential_id, c.cloud_ref, cc.provider, c.rbac_mode,
	c.agent_rbac_hash, c.agent_rbac_applied_at, c.agent_rbac_error`
const clusterListFrom = ` FROM clusters c LEFT JOIN cloud_credentials cc ON cc.id = c.cloud_credential_id`

func scanClusterRow(row pgx.Row) (clusterRow, error) {
	var r clusterRow
	err := row.Scan(&r.id, &r.displayName, &r.description, &r.apiEndpoint, &r.enabled, &r.insecureSkipTLSVerify, &r.hasClientKey,
		&r.connector, &r.agentLastSeen, &r.kind, &r.cloudCredentialID, &r.cloudRef, &r.cloudProvider, &r.rbacMode,
		&r.agentRBACHash, &r.agentRBACAppliedAt, &r.agentRBACError)
	return r, err
}

// List returns the configured clusters (without credentials). A user with a
// global role (admin or readonly) sees all of them; a user without a global
// role but with 'cluster'-scoped assignments sees only those — otherwise the
// Dashboard would show cards for clusters they cannot enter afterwards.
func (h *ClusterHandler) List(w http.ResponseWriter, r *http.Request) {
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	role, _ := middleware.RoleFromContext(r.Context())

	var rows pgx.Rows
	var err error
	if role != "" {
		rows, err = h.DB.Query(r.Context(), `SELECT `+clusterListColumns+clusterListFrom+` ORDER BY c.display_name`)
	} else {
		rows, err = h.DB.Query(r.Context(),
			`SELECT `+clusterListColumns+clusterListFrom+`
			 JOIN role_assignments ra ON ra.scope_type = 'cluster' AND ra.scope_ref = c.id::text
			 WHERE ra.user_id = $1
			 ORDER BY c.display_name`,
			sess.UserID,
		)
	}
	if err != nil {
		h.Logger.Error("clusters.list: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var list []clusterRow
	for rows.Next() {
		row, err := scanClusterRow(rows)
		if err != nil {
			h.Logger.Error("clusters.list: failed to read row", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		list = append(list, row)
	}
	rows.Close()

	out := make([]clusterResponse, 0, len(list))
	for _, row := range list {
		out = append(out, h.toResponse(r.Context(), sess.UserID, row))
	}
	writeJSON(w, http.StatusOK, out)
}

// clusterInput is the create and edit body. On edit every field is optional
// (pointers): only what is sent is touched. A kubeconfig, if present, replaces
// endpoint/CA/credential in one go.
type clusterInput struct {
	DisplayName           *string `json:"displayName"`
	Description           *string `json:"description"`
	APIEndpoint           *string `json:"apiEndpoint"`
	CACert                *string `json:"caCert"`
	InsecureSkipTLSVerify *bool   `json:"insecureSkipTLSVerify"`
	SAToken               *string `json:"saToken"`
	ClientCert            *string `json:"clientCert"`
	ClientKey             *string `json:"clientKey"`
	Kubeconfig            *string `json:"kubeconfig"`
	Enabled               *bool   `json:"enabled"`
	// Connector "agent" on creation: no credentials; the agent key and the
	// manifest to apply in the cluster are generated.
	Connector string `json:"connector"`
	// Kind "docker": Docker/Podman host (always via agent).
	Kind string `json:"kind"`
	// Connector "cloud": provider credential + reference of the discovered
	// cluster (see internal/cloud). No credentials of its own.
	CloudCredentialID *int64     `json:"cloudCredentialId"`
	CloudRef          *cloud.Ref `json:"cloudRef"`
	// RBACMode: "scoped" (default) or "cluster-admin" (see migration 0026).
	// It only affects the RBAC the panel generates (agent and cloud).
	RBACMode *string `json:"rbacMode"`
}

const (
	rbacModeScoped       = "scoped"
	rbacModeClusterAdmin = "cluster-admin"
)

func validRBACMode(m string) bool { return m == rbacModeScoped || m == rbacModeClusterAdmin }

func trimPtr(p *string) {
	if p != nil {
		// Copy/pasting from a terminal almost always adds a trailing newline
		// (and sometimes spaces). A token ending in "\n" breaks the
		// "Authorization: Bearer <token>\n" header with an opaque net/http
		// error, so it is sanitised here instead of demanding that the admin
		// paste perfectly.
		*p = strings.TrimSpace(*p)
	}
}

func (in *clusterInput) trim() {
	trimPtr(in.DisplayName)
	trimPtr(in.Description)
	trimPtr(in.APIEndpoint)
	trimPtr(in.CACert)
	trimPtr(in.SAToken)
	trimPtr(in.ClientCert)
	trimPtr(in.ClientKey)
	trimPtr(in.Kubeconfig)
}

// touchesConnection says whether the body changes anything that forces a
// revalidation and reconnection (everything but name, description and enabled).
func (in *clusterInput) touchesConnection() bool {
	return in.APIEndpoint != nil || in.CACert != nil || in.InsecureSkipTLSVerify != nil ||
		in.SAToken != nil || in.ClientCert != nil || in.ClientKey != nil || in.Kubeconfig != nil
}

// applyTo merges the body onto existing credentials (empty on creation). A
// kubeconfig wins over the loose fields; a new token discards the previous
// client certificate and vice versa, so as not to end up with two half
// credentials.
func (in *clusterInput) applyTo(creds k8s.Credentials) (k8s.Credentials, error) {
	if in.Kubeconfig != nil && *in.Kubeconfig != "" {
		parsed, err := k8s.ParseKubeconfig(*in.Kubeconfig)
		if err != nil {
			return creds, err
		}
		creds = parsed
	}
	if in.APIEndpoint != nil {
		creds.APIEndpoint = *in.APIEndpoint
	}
	if in.CACert != nil {
		creds.CACert = *in.CACert
	}
	if in.InsecureSkipTLSVerify != nil {
		creds.Insecure = *in.InsecureSkipTLSVerify
	}
	if in.SAToken != nil && *in.SAToken != "" {
		creds.Token = *in.SAToken
		creds.ClientCert, creds.ClientKey = "", ""
	}
	if in.ClientKey != nil && *in.ClientKey != "" {
		creds.ClientKey = *in.ClientKey
		if in.ClientCert != nil {
			creds.ClientCert = *in.ClientCert
		}
		creds.Token = ""
	}
	return creds, nil
}

// encryptCreds encrypts what needs encrypting and returns the values ready for
// the row. Unused columns go to NULL (nil) to satisfy the CHECKs.
func (h *ClusterHandler) encryptCreds(creds k8s.Credentials) (token []byte, clientKey []byte, clientCert *string, caCert *string, keyID string, err error) {
	if creds.ClientKey != "" {
		keyID, clientKey, err = h.Keyring.Encrypt([]byte(creds.ClientKey))
		if err != nil {
			return
		}
		cc := creds.ClientCert
		clientCert = &cc
	} else {
		keyID, token, err = h.Keyring.Encrypt([]byte(creds.Token))
		if err != nil {
			return
		}
	}
	if creds.CACert != "" {
		ca := creds.CACert
		caCert = &ca
	}
	return
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Create validates the credentials against the real cluster, persists them
// encrypted and starts its informer cache. Admin only.
func (h *ClusterHandler) Create(w http.ResponseWriter, r *http.Request) {
	sess, _ := middleware.SessionFromContext(r.Context())
	var in clusterInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	in.trim()
	if in.DisplayName == nil || *in.DisplayName == "" {
		http.Error(w, "displayName is required", http.StatusBadRequest)
		return
	}

	if in.Kind == "docker" {
		in.Connector = "agent"
	}
	if in.Connector == "agent" {
		h.createAgentCluster(w, r, sess, in)
		return
	}
	if in.Connector == "cloud" {
		h.createCloudCluster(w, r, sess, in)
		return
	}

	creds, err := in.applyTo(k8s.Credentials{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	validateCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	version, err := k8s.ValidateCredentials(validateCtx, creds)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	token, clientKey, clientCert, caCert, keyID, err := h.encryptCreds(creds)
	if err != nil {
		h.Logger.Error("clusters.create: failed to encrypt credentials", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var id int64
	err = h.DB.QueryRow(r.Context(),
		`INSERT INTO clusters (display_name, description, api_endpoint, ca_cert, insecure_skip_tls_verify,
		                       sa_token_encrypted, client_cert, client_key_encrypted, key_id)
		 VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		*in.DisplayName, deref(in.Description), creds.APIEndpoint, caCert, creds.Insecure,
		token, clientCert, clientKey, keyID,
	).Scan(&id)
	if err != nil {
		// Discriminate the duplicate instead of returning 409 with the raw
		// Postgres error for any failure: a 409 has to mean a name conflict,
		// not "something went wrong".
		if isUniqueViolation(err) {
			http.Error(w, "a cluster with that name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("clusters.create: failed to insert cluster", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.reconnect(r.Context(), id, "clusters.create")
	h.auditCluster(r, "cluster.create", *in.DisplayName, map[string]any{"endpoint": creds.APIEndpoint, "authType": creds.AuthType(), "serverVersion": version, "insecure": creds.Insecure})

	row, err := scanClusterRow(h.DB.QueryRow(r.Context(), `SELECT `+clusterListColumns+clusterListFrom+` WHERE c.id = $1`, id))
	if err != nil {
		h.Logger.Error("clusters.create: failed to re-read cluster", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, h.toResponse(r.Context(), sess.UserID, row))
}

// reconnect reloads the row and (re)starts the connection. A failure is logged
// but does not break the request: the cluster stays in the DB and the UI
// reports its status.
func (h *ClusterHandler) reconnect(ctx context.Context, id int64, op string) {
	cfg, err := h.K8s.LoadCluster(ctx, id)
	if err != nil {
		h.Logger.Error(op+": failed to reload cluster", "error", err)
		return
	}
	if cfg.Kind == "docker" {
		if h.Docker != nil {
			if err := h.Docker.Connect(id, cfg.DisplayName); err != nil && !errors.Is(err, dockerenv.ErrHostOffline) {
				h.Logger.Error(op+": failed to connect docker host", "error", err)
			}
		}
		return
	}
	if err := h.K8s.Connect(cfg); err != nil && !errors.Is(err, k8s.ErrAgentOffline) {
		h.Logger.Error(op+": failed to connect cluster", "error", err)
	}
}

// Update edits a cluster: name, description, enabled and/or credentials. If
// anything about the connection changes, the resulting credentials are
// validated against the cluster BEFORE being saved and the cluster is
// reconnected — this is how a token is rotated without deleting the cluster
// (and without losing its role assignments, which used to be the only way).
func (h *ClusterHandler) Update(w http.ResponseWriter, r *http.Request) {
	sess, _ := middleware.SessionFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in clusterInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	in.trim()
	if in.DisplayName != nil && *in.DisplayName == "" {
		http.Error(w, "displayName cannot be empty", http.StatusBadRequest)
		return
	}

	current, err := h.K8s.LoadCluster(r.Context(), id)
	if errors.Is(err, k8s.ErrClusterNotFound) {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("clusters.update: failed to read cluster", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var wasEnabled bool
	if err := h.DB.QueryRow(r.Context(), `SELECT enabled FROM clusters WHERE id = $1`, id).Scan(&wasEnabled); err != nil {
		h.Logger.Error("clusters.update: failed to read enabled", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	detail := map[string]any{}
	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		h.Logger.Error("clusters.update: failed to begin transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	if in.RBACMode != nil {
		if !validRBACMode(*in.RBACMode) {
			http.Error(w, "rbacMode must be scoped or cluster-admin", http.StatusBadRequest)
			return
		}
		if current.Connector == "direct" {
			http.Error(w, "rbacMode only applies to agent or cloud clusters: on a direct cluster you apply the RBAC yourself", http.StatusBadRequest)
			return
		}
	}
	if current.Connector == k8s.ConnectorInCluster && (in.touchesConnection() || (in.Enabled != nil && !*in.Enabled)) {
		http.Error(w, "the local cluster is the one running the server: it has no credentials to edit and cannot be disabled", http.StatusBadRequest)
		return
	}
	if in.touchesConnection() && current.Connector == "agent" {
		http.Error(w, "an agent cluster has no credentials to edit: regenerate the manifest if needed", http.StatusBadRequest)
		return
	}
	if in.touchesConnection() && current.Connector == "cloud" {
		http.Error(w, "a cloud cluster takes endpoint and credential from the provider: edit the cloud credential instead", http.StatusBadRequest)
		return
	}
	if in.touchesConnection() {
		creds, err := h.K8s.Decrypt(current)
		if err != nil {
			h.Logger.Error("clusters.update: failed to decrypt current credentials", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		creds, err = in.applyTo(creds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		validateCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		version, err := k8s.ValidateCredentials(validateCtx, creds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		token, clientKey, clientCert, caCert, keyID, err := h.encryptCreds(creds)
		if err != nil {
			h.Logger.Error("clusters.update: failed to encrypt credentials", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE clusters SET api_endpoint = $2, ca_cert = $3, insecure_skip_tls_verify = $4,
			        sa_token_encrypted = $5, client_cert = $6, client_key_encrypted = $7, key_id = $8, updated_at = now()
			 WHERE id = $1`,
			id, creds.APIEndpoint, caCert, creds.Insecure, token, clientCert, clientKey, keyID,
		); err != nil {
			h.Logger.Error("clusters.update: failed to save credentials", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		detail["credentialsRotated"] = true
		detail["endpoint"] = creds.APIEndpoint
		detail["authType"] = creds.AuthType()
		detail["serverVersion"] = version
		detail["insecure"] = creds.Insecure
	}
	if in.DisplayName != nil && *in.DisplayName != current.DisplayName {
		if _, err := tx.Exec(r.Context(), `UPDATE clusters SET display_name = $2, updated_at = now() WHERE id = $1`, id, *in.DisplayName); err != nil {
			if isUniqueViolation(err) {
				http.Error(w, "a cluster with that name already exists", http.StatusConflict)
				return
			}
			h.Logger.Error("clusters.update: rename failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		detail["renamedFrom"] = current.DisplayName
		detail["renamedTo"] = *in.DisplayName
	}
	if in.RBACMode != nil {
		if _, err := tx.Exec(r.Context(), `UPDATE clusters SET rbac_mode = $2, updated_at = now() WHERE id = $1`, id, *in.RBACMode); err != nil {
			h.Logger.Error("clusters.update: failed to save rbac_mode", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		detail["rbacMode"] = *in.RBACMode
	}
	if in.Description != nil {
		if _, err := tx.Exec(r.Context(), `UPDATE clusters SET description = NULLIF($2, ''), updated_at = now() WHERE id = $1`, id, *in.Description); err != nil {
			h.Logger.Error("clusters.update: failed to save description", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	enabled := wasEnabled
	if in.Enabled != nil && *in.Enabled != wasEnabled {
		if _, err := tx.Exec(r.Context(), `UPDATE clusters SET enabled = $2, updated_at = now() WHERE id = $1`, id, *in.Enabled); err != nil {
			h.Logger.Error("clusters.update: failed to change enabled", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		enabled = *in.Enabled
		detail["enabled"] = enabled
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.Logger.Error("clusters.update: failed to commit transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Reconnection outside the transaction, with the row already final.
	switch {
	case !enabled:
		h.K8s.Disconnect(id)
		if h.Docker != nil {
			h.Docker.Disconnect(id)
		}
		if h.AgentAuth != nil {
			h.AgentAuth.Evict(id)
		}
	case in.touchesConnection() || !wasEnabled || (in.DisplayName != nil && *in.DisplayName != current.DisplayName):
		h.reconnect(r.Context(), id, "clusters.update")
	}

	action := "cluster.update"
	if in.Enabled != nil && *in.Enabled != wasEnabled {
		action = "cluster.enable"
		if !enabled {
			action = "cluster.disable"
		}
	}
	name := current.DisplayName
	if in.DisplayName != nil {
		name = *in.DisplayName
	}
	h.auditCluster(r, action, name, detail)

	row, err := scanClusterRow(h.DB.QueryRow(r.Context(), `SELECT `+clusterListColumns+clusterListFrom+` WHERE c.id = $1`, id))
	if err != nil {
		h.Logger.Error("clusters.update: failed to re-read cluster", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.toResponse(r.Context(), sess.UserID, row))
}

// Delete removes a managed cluster entirely: its 'cluster'-scoped role
// assignments (and group mappings of the same scope), the clusters row, and it
// disconnects its in-memory informers. Global admin only (like Create): it is
// platform management, not an action on a resource inside an existing cluster.
//
// role_assignments.scope_ref and group_role_mappings.*_ref are TEXT without an
// FK to clusters.id (validated at application level) — without this explicit
// deletion, orphan rows pointing at a non-existent cluster would remain.
// audit_log.cluster stores the display_name (not the id) and is immutable, so
// the audit history needs no change here.
//
// Accepted limitations: a concurrent call to Update(enabled=true) on the same
// id could reconnect informers right after this deletion; and a logs/exec
// session already open on this cluster stays alive until the client itself
// closes the WebSocket, because Disconnect does not cancel its context. Both
// cases are global-admin and rare.
func (h *ClusterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		h.Logger.Error("clusters.delete: failed to begin transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	// The name is read INSIDE the transaction and with FOR UPDATE: renaming
	// exists now, and the audit entry must carry the name it was deleted under.
	var displayName, connector string
	err = tx.QueryRow(r.Context(), `SELECT display_name, connector FROM clusters WHERE id = $1 FOR UPDATE`, id).Scan(&displayName, &connector)
	if err == nil && connector == k8s.ConnectorInCluster {
		http.Error(w, "the local cluster cannot be deleted: it is where the server runs (disable LOCAL_CLUSTER in the chart if you do not want it)", http.StatusConflict)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("clusters.delete: failed to query cluster", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	scopeRef := strconv.FormatInt(id, 10)
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM role_assignments WHERE scope_type = 'cluster' AND scope_ref = $1`, scopeRef,
	); err != nil {
		h.Logger.Error("clusters.delete: failed to delete role_assignments", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM group_role_mappings WHERE scope_type = 'cluster' AND scope_ref = $1`, scopeRef,
	); err != nil {
		h.Logger.Error("clusters.delete: failed to delete group_role_mappings", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM clusters WHERE id = $1`, id); err != nil {
		h.Logger.Error("clusters.delete: failed to delete cluster", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.Logger.Error("clusters.delete: failed to commit transaction", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.K8s.Disconnect(id)
	if h.Docker != nil {
		h.Docker.Disconnect(id)
	}
	if h.AgentAuth != nil {
		h.AgentAuth.Evict(id)
	}
	h.auditCluster(r, "cluster.delete", displayName, nil)

	w.WriteHeader(http.StatusNoContent)
}

func (h *ClusterHandler) auditCluster(r *http.Request, action, clusterName string, detail map[string]any) {
	sess, _ := middleware.SessionFromContext(r.Context())
	var actorID *int64
	if sess != nil {
		actorID = &sess.UserID
	}
	if len(detail) == 0 {
		detail = nil
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: actorID, IP: middleware.ClientIP(r), Action: action, Cluster: clusterName, Result: audit.ResultSuccess, Detail: detail,
	})
}
