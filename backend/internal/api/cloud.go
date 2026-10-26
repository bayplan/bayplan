package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/cloud"
	"github.com/bayplan/bayplan/internal/middleware"
)

// CloudHandler manages cloud provider credentials (global admin only) and the
// discovery of clusters with them. Secrets are validated against the provider
// before being stored and are never returned: the response carries
// `secretSet: true`, just like the SSO configuration does.
type CloudHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	Cloud  *cloud.Service
}

type cloudCredentialResponse struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	Provider     cloud.Provider `json:"provider"`
	Config       cloud.Config   `json:"config"`
	SecretSet    bool           `json:"secretSet"`
	ClusterCount int            `json:"clusterCount"`
	CreatedBy    string         `json:"createdBy,omitempty"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

func toCloudResponse(c cloud.Credential) cloudCredentialResponse {
	return cloudCredentialResponse{
		ID: c.ID, Name: c.Name, Provider: c.Provider, Config: c.Config, SecretSet: true,
		ClusterCount: c.ClusterCount, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// cloudCredentialInput: create and edit. On edit everything is optional; an
// empty or omitted secret keeps the current one.
type cloudCredentialInput struct {
	Name     *string        `json:"name"`
	Provider cloud.Provider `json:"provider"`
	Config   *cloud.Config  `json:"config"`
	Secret   *cloud.Secret  `json:"secret"`
}

func (in *cloudCredentialInput) secretGiven() bool {
	return in.Secret != nil && (in.Secret.SecretAccessKey != "" || in.Secret.ClientSecret != "" || in.Secret.ServiceAccountJSON != "")
}

func (h *CloudHandler) List(w http.ResponseWriter, r *http.Request) {
	creds, err := h.Cloud.Store.List(r.Context())
	if err != nil {
		h.Logger.Error("cloud.list: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]cloudCredentialResponse, 0, len(creds))
	for _, c := range creds {
		out = append(out, toCloudResponse(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *CloudHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in cloudCredentialInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if in.Name == nil || in.Config == nil || in.Secret == nil {
		http.Error(w, "name, provider, config and secret are required", http.StatusBadRequest)
		return
	}
	cred := &cloud.Credential{Name: *in.Name, Provider: in.Provider, Config: *in.Config, Secret: *in.Secret}
	if sess, ok := middleware.SessionFromContext(r.Context()); ok {
		cred.CreatedBy = sess.Username
	}
	if err := h.validate(r.Context(), cred); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	id, err := h.Cloud.Store.Create(r.Context(), cred)
	if errors.Is(err, cloud.ErrDuplicate) {
		http.Error(w, "a credential with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		h.Logger.Error("cloud.create: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cred.ID = id
	h.auditCred(r, "cloud_credential.create", cred)
	writeJSON(w, http.StatusCreated, toCloudResponse(*cred))
}

// validate normalises and checks the credential against the provider.
func (h *CloudHandler) validate(ctx context.Context, cred *cloud.Credential) error {
	cred.Config.Identity = ""
	// Regions arrive as a list; drop empties from a pasted "eu-west-1, ".
	regions := cred.Config.Regions[:0]
	for _, r := range cred.Config.Regions {
		if r != "" {
			regions = append(regions, r)
		}
	}
	cred.Config.Regions = regions
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return h.Cloud.Validate(ctx, cred)
}

func (h *CloudHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in cloudCredentialInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	cred, err := h.Cloud.Store.Get(r.Context(), id)
	if errors.Is(err, cloud.ErrNotFound) {
		http.Error(w, "credential not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("cloud.update: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if in.Name != nil {
		cred.Name = *in.Name
	}
	// The provider is not changed: an AWS credential with EKS clusters hanging
	// off it cannot become an Azure one.
	revalidate := false
	if in.Config != nil {
		cred.Config = *in.Config
		revalidate = true
	}
	if in.secretGiven() {
		cred.Secret = *in.Secret
		revalidate = true
	}
	if revalidate {
		if err := h.validate(r.Context(), cred); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	} else if err := cred.Validate(false); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Cloud.Store.Update(r.Context(), cred); err != nil {
		if errors.Is(err, cloud.ErrDuplicate) {
			http.Error(w, "a credential with that name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("cloud.update: save failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.auditCred(r, "cloud_credential.update", cred)
	writeJSON(w, http.StatusOK, toCloudResponse(*cred))
}

func (h *CloudHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	cred, err := h.Cloud.Store.Get(r.Context(), id)
	if errors.Is(err, cloud.ErrNotFound) {
		http.Error(w, "credential not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("cloud.delete: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch err := h.Cloud.Store.Delete(r.Context(), id); {
	case errors.Is(err, cloud.ErrInUse):
		http.Error(w, "the credential has clusters attached: delete them first", http.StatusConflict)
		return
	case errors.Is(err, cloud.ErrNotFound):
		http.Error(w, "credential not found", http.StatusNotFound)
		return
	case err != nil:
		h.Logger.Error("cloud.delete: delete failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.auditCred(r, "cloud_credential.delete", cred)
	w.WriteHeader(http.StatusNoContent)
}

// Discover lists the provider's clusters visible with the credential.
func (h *CloudHandler) Discover(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	cred, err := h.Cloud.Store.Get(r.Context(), id)
	if errors.Is(err, cloud.ErrNotFound) {
		http.Error(w, "credential not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("cloud.discover: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	found, err := h.Cloud.Discover(ctx, cred)
	if err != nil {
		// The provider's error is the real explanation (missing IAM permission,
		// non-existent region…): it is returned verbatim.
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (h *CloudHandler) auditCred(r *http.Request, action string, cred *cloud.Credential) {
	var actorID *int64
	if sess, ok := middleware.SessionFromContext(r.Context()); ok {
		actorID = &sess.UserID
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: actorID, IP: middleware.ClientIP(r), Action: action, Result: audit.ResultSuccess,
		Detail: map[string]any{"name": cred.Name, "provider": cred.Provider, "identity": cred.Config.Identity},
	})
}
