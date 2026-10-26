package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bayplan/bayplan/internal/agent"
	"github.com/bayplan/bayplan/internal/cloud"
	"github.com/bayplan/bayplan/internal/session"
)

// Creation of clusters with the cloud connector (see internal/cloud): the admin
// picks a provider credential, discovers its clusters and adds one. The server
// describes the cluster at the provider, checks it reaches its API server with
// the credential's identity and stores endpoint + CA + reference; there is no
// cluster credential as such: a fresh token is issued every time.

// cloudOnboarding is what the UI shows after creation: the RBAC to apply in the
// cluster for the provider's identity. Without it, the API server answers 403
// to everything (unless that identity is already cluster-admin, like the
// creator of an EKS) and the cluster shows as "connected" but with no data.
type cloudOnboarding struct {
	Cluster  clusterResponse `json:"cluster"`
	Provider cloud.Provider  `json:"provider"`
	// Identity is what the provider attributes to the credential (ARN, oid, email).
	Identity string `json:"identity"`
	// SubjectKind/SubjectName is who the YAML grants permissions to.
	SubjectKind string `json:"subjectKind"`
	SubjectName string `json:"subjectName"`
	RBACYAML    string `json:"rbacYaml"`
}

func (h *ClusterHandler) createCloudCluster(w http.ResponseWriter, r *http.Request, sess *session.Session, in clusterInput) {
	if h.Cloud == nil {
		http.Error(w, "the cloud connector is not enabled", http.StatusBadRequest)
		return
	}
	if in.CloudCredentialID == nil || in.CloudRef == nil || in.CloudRef.Name == "" {
		http.Error(w, "a cloud cluster needs cloudCredentialId and cloudRef (discover it from the credential)", http.StatusBadRequest)
		return
	}
	cred, err := h.Cloud.Store.Get(r.Context(), *in.CloudCredentialID)
	if errors.Is(err, cloud.ErrNotFound) {
		http.Error(w, "cloud credential not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		h.Logger.Error("clusters.createCloud: failed to load credential", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 40 s: describe at the provider + /version on the API server. Longer than
	// direct creation because there are two hops, and a private cluster takes
	// a while to fail.
	probeCtx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	cfg, version, err := h.Cloud.Probe(probeCtx, cred, *in.CloudRef)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	refJSON, _ := json.Marshal(in.CloudRef)
	rbacMode := rbacModeScoped
	if in.RBACMode != nil {
		if !validRBACMode(*in.RBACMode) {
			http.Error(w, "rbacMode must be scoped or cluster-admin", http.StatusBadRequest)
			return
		}
		rbacMode = *in.RBACMode
	}
	var id int64
	err = h.DB.QueryRow(r.Context(),
		`INSERT INTO clusters (display_name, description, api_endpoint, ca_cert, insecure_skip_tls_verify, connector, cloud_credential_id, cloud_ref, rbac_mode)
		 VALUES ($1, NULLIF($2, ''), $3, $4, FALSE, 'cloud', $5, $6, $7) RETURNING id`,
		*in.DisplayName, deref(in.Description), cfg.Host, string(cfg.TLSClientConfig.CAData), cred.ID, refJSON, rbacMode,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			http.Error(w, "a cluster with that name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("clusters.createCloud: failed to insert cluster", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.reconnect(r.Context(), id, "clusters.createCloud")
	h.auditCluster(r, "cluster.create", *in.DisplayName, map[string]any{
		"endpoint": cfg.Host, "authType": "cloud", "provider": cred.Provider, "credential": cred.Name, "ref": in.CloudRef, "serverVersion": version,
	})

	row, err := scanClusterRow(h.DB.QueryRow(r.Context(), `SELECT `+clusterListColumns+clusterListFrom+` WHERE c.id = $1`, id))
	if err != nil {
		h.Logger.Error("clusters.createCloud: failed to re-read cluster", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, h.cloudOnboarding(r, sess, row, cred))
}

func (h *ClusterHandler) cloudOnboarding(r *http.Request, sess *session.Session, row clusterRow, cred *cloud.Credential) cloudOnboarding {
	var uid int64
	if sess != nil {
		uid = sess.UserID
	}
	kind, name := h.Cloud.Subject(cred)
	return cloudOnboarding{
		Cluster:     h.toResponse(r.Context(), uid, row),
		Provider:    cred.Provider,
		Identity:    cred.Config.Identity,
		SubjectKind: kind,
		SubjectName: name,
		RBACYAML:    agent.RenderRBAC(agent.Subject{Kind: kind, Name: name}, row.rbacMode == rbacModeClusterAdmin),
	}
}

// CloudRBAC serves a cloud cluster's onboarding again (RBAC to apply), for when
// the modal was closed without applying it or it is reapplied after a panel
// upgrade that adds permissions. Admin only.
func (h *ClusterHandler) CloudRBAC(w http.ResponseWriter, r *http.Request) {
	if h.Cloud == nil {
		http.Error(w, "the cloud connector is not enabled", http.StatusNotFound)
		return
	}
	sess, _ := sessionFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	row, err := scanClusterRow(h.DB.QueryRow(r.Context(), `SELECT `+clusterListColumns+clusterListFrom+` WHERE c.id = $1`, id))
	if err != nil || row.connector != "cloud" || row.cloudCredentialID == nil {
		http.Error(w, "cloud cluster not found", http.StatusNotFound)
		return
	}
	cred, err := h.Cloud.Store.Get(r.Context(), *row.cloudCredentialID)
	if err != nil {
		h.Logger.Error("clusters.cloudRBAC: failed to load credential", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.cloudOnboarding(r, sess, row, cred))
}
