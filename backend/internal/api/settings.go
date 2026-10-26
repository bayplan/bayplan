package api

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/settings"
)

// SettingsHandler exposes the hot-editable instance configuration (see
// internal/settings). Only a global admin reads and writes it.
type SettingsHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	Store  *settings.Store
	// OIDC is optional: if present, its sections are served and edited from
	// here (see settings_oidc.go). Nil while there is no configurable provider.
	OIDC OIDCSettingsSource
	// TLSMode is how the binary serves HTTPS itself ("" = it does not; see
	// tlsserve). The UI uses it to explain whether agents get a CA on their own.
	TLSMode string
	// AgentCA is the CA the server already puts in the agent manifests on its
	// own (built-in self-signed HTTPS or the chart's cert-manager CA), if any.
	AgentCA func() []byte
}

// OIDCSettingsSource is what SettingsHandler needs from the identity provider
// to show and apply its configuration, without coupling this file to the oidc
// package.
type OIDCSettingsSource interface {
	// Public returns the OIDC configuration with the secret masked.
	Public(r *http.Request) (any, error)
	// Apply validates and saves the configuration received from the admin.
	Apply(r *http.Request, body json.RawMessage, actor string) (any, error)
}

type settingsResponse struct {
	Server settings.Server `json:"server"`
	// ServerVersion and DefaultAgentImage are informative: the UI shows which
	// agent image will be used if agentImage is left empty.
	ServerVersion     string             `json:"serverVersion"`
	DefaultAgentImage string             `json:"defaultAgentImage"`
	TLSMode           string             `json:"tlsMode"`
	AgentCAAuto       bool               `json:"agentCAAuto"`
	OIDC              any                `json:"oidc,omitempty"`
	SMTP              smtpResponse       `json:"smtp"`
	Costs             settings.Costs     `json:"costs"`
	AI                aiSettingsResponse `json:"ai"`
}

// smtpResponse masks the password, just like OIDC with its client secret.
type smtpResponse struct {
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	PasswordSet        bool   `json:"passwordSet"`
	From               string `json:"from"`
	StartTLS           bool   `json:"startTLS"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
}

func toSMTPResponse(s settings.SMTP) smtpResponse {
	return smtpResponse{Host: s.Host, Port: s.Port, Username: s.Username, PasswordSet: s.Password.IsSet(), From: s.From, StartTLS: s.StartTLS, InsecureSkipVerify: s.InsecureSkipVerify}
}

// smtpInput: the password travels in the clear only towards the server; empty
// or omitted keeps the current one.
type smtpInput struct {
	Host               string  `json:"host"`
	Port               int     `json:"port"`
	Username           string  `json:"username"`
	Password           *string `json:"password"`
	From               string  `json:"from"`
	StartTLS           bool    `json:"startTLS"`
	InsecureSkipVerify bool    `json:"insecureSkipVerify"`
}

// Get returns every section with the secrets masked. Admin only.
func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	var resp settingsResponse
	if err := h.Store.Get(r.Context(), settings.KeyServer, &resp.Server); err != nil {
		h.Logger.Error("settings.get: failed reading server", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var smtp settings.SMTP
	if err := h.Store.Get(r.Context(), settings.KeySMTP, &smtp); err != nil {
		h.Logger.Error("settings.get: failed reading smtp", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp.SMTP = toSMTPResponse(smtp)
	resp.ServerVersion = settings.ServerVersion
	resp.DefaultAgentImage = settings.DefaultAgentImage()
	resp.TLSMode = h.TLSMode
	resp.AgentCAAuto = h.AgentCA != nil && len(h.AgentCA()) > 0
	if err := h.Store.Get(r.Context(), settings.KeyCosts, &resp.Costs); err != nil {
		h.Logger.Error("settings.get: failed reading costs", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var aiSettings settings.AI
	if err := h.Store.Get(r.Context(), settings.KeyAI, &aiSettings); err != nil {
		h.Logger.Error("settings.get: failed reading ai", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp.AI = toAISettingsResponse(aiSettings)
	if h.OIDC != nil {
		oidc, err := h.OIDC.Public(r)
		if err != nil {
			h.Logger.Error("settings.get: failed reading oidc", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		resp.OIDC = oidc
	}
	writeJSON(w, http.StatusOK, resp)
}

// Update saves one section: PUT /api/settings/{key}. Each section validates its
// own; an unknown key is 404 and not "save whatever comes", so that a typo in
// the client does not create phantom sections.
func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	key := r.PathValue("key")

	var body json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var (
		stored any
		detail map[string]any
	)
	switch key {
	case settings.KeyServer:
		var s settings.Server
		if err := json.Unmarshal(body, &s); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		s.PublicURL = strings.TrimRight(strings.TrimSpace(s.PublicURL), "/")
		s.AgentImage = strings.TrimSpace(s.AgentImage)
		if s.PublicURL != "" {
			u, err := url.Parse(s.PublicURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				http.Error(w, "publicURL must be an absolute http(s) URL", http.StatusBadRequest)
				return
			}
		}
		s.AgentCACert = strings.TrimSpace(s.AgentCACert)
		if s.AgentCACert != "" && !validCertPEM(s.AgentCACert) {
			http.Error(w, "agentCACert must be one or more PEM certificates", http.StatusBadRequest)
			return
		}
		if err := h.Store.Set(r.Context(), key, s, sess.Username); err != nil {
			h.Logger.Error("settings.update: failed saving server", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		stored = s
		detail = map[string]any{"publicURL": s.PublicURL, "agentCACert": s.AgentCACert != ""}

	case settings.KeyCosts:
		var c settings.Costs
		if err := json.Unmarshal(body, &c); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		c.Currency = strings.TrimSpace(c.Currency)
		if c.CPUCoreHour < 0 || c.MemoryGiBHour < 0 || c.CPUCoreHour > 1e6 || c.MemoryGiBHour > 1e6 {
			http.Error(w, "prices must be non-negative numbers", http.StatusBadRequest)
			return
		}
		if len(c.Currency) > 8 {
			http.Error(w, "currency too long", http.StatusBadRequest)
			return
		}
		if c.Currency == "" {
			c.Currency = "EUR"
		}
		if err := h.Store.Set(r.Context(), key, c, sess.Username); err != nil {
			h.Logger.Error("settings.update: failed saving costs", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		stored = c
		detail = map[string]any{"cpuCoreHour": c.CPUCoreHour, "memoryGiBHour": c.MemoryGiBHour, "currency": c.Currency}

	case settings.KeySMTP:
		var in smtpInput
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		var current settings.SMTP
		if err := h.Store.Get(r.Context(), key, &current); err != nil {
			h.Logger.Error("settings.update: failed reading smtp", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s := settings.SMTP{
			Host: strings.TrimSpace(in.Host), Port: in.Port, Username: strings.TrimSpace(in.Username),
			From: strings.TrimSpace(in.From), StartTLS: in.StartTLS, InsecureSkipVerify: in.InsecureSkipVerify,
			Password: current.Password,
		}
		if in.Port < 0 || in.Port > 65535 {
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}
		if s.Host != "" && s.From == "" {
			http.Error(w, "from is required when there is an SMTP server", http.StatusBadRequest)
			return
		}
		if in.Password != nil && *in.Password != "" {
			sec, err := h.Store.EncryptSecret(*in.Password)
			if err != nil {
				h.Logger.Error("settings.update: failed encrypting smtp password", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			s.Password = sec
		}
		if s.Username == "" {
			s.Password = settings.Secret{}
		}
		if err := h.Store.Set(r.Context(), key, s, sess.Username); err != nil {
			h.Logger.Error("settings.update: failed saving smtp", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		stored = toSMTPResponse(s)
		detail = map[string]any{"host": s.Host, "from": s.From}

	case settings.KeyAI:
		var in aiSettingsInput
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		var current settings.AI
		if err := h.Store.Get(r.Context(), key, &current); err != nil {
			h.Logger.Error("settings.update: failed reading ai", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		a, err := buildAISettings(in, current, h.Store.EncryptSecret)
		var verr *ValidationError
		if errors.As(err, &verr) {
			http.Error(w, verr.Msg, http.StatusBadRequest)
			return
		}
		if err != nil {
			h.Logger.Error("settings.update: failed preparing ai", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := h.Store.Set(r.Context(), key, a, sess.Username); err != nil {
			h.Logger.Error("settings.update: failed saving ai", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		stored = toAISettingsResponse(a)
		// Never the key, not even whether it changed in a way that hints at it.
		detail = map[string]any{"enabled": a.Enabled, "provider": a.Provider, "model": a.Model, "baseURL": a.BaseURL, "minRole": a.EffectiveMinRole(), "dailyLimitPerUser": a.EffectiveDailyLimit(), "apiKeyChanged": in.ClearAPIKey || (in.APIKey != nil && *in.APIKey != "")}

	case settings.KeyOIDC:
		if h.OIDC == nil {
			http.Error(w, "section not available", http.StatusNotFound)
			return
		}
		out, err := h.OIDC.Apply(r, body, sess.Username)
		if err != nil {
			var ve *ValidationError
			if errors.As(err, &ve) {
				http.Error(w, ve.Error(), http.StatusBadRequest)
				return
			}
			h.Logger.Error("settings.update: failed saving oidc", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		stored = out
		detail = map[string]any{"section": key}

	default:
		http.Error(w, "unknown section", http.StatusNotFound)
		return
	}

	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, ActorUsername: sess.Username, IP: middleware.ClientIP(r),
		Action: "settings.update", Resource: key, Result: audit.ResultSuccess, Detail: detail,
	})
	writeJSON(w, http.StatusOK, stored)
}

// ValidationError is an input validation error of the admin: it is returned to
// the client as is (400), unlike an internal failure.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// validCertPEM reports whether s holds at least one parseable certificate and
// nothing but certificates: a private key pasted by mistake must not end up in
// every agent manifest.
func validCertPEM(s string) bool {
	rest := []byte(s)
	n := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return false
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return false
		}
		n++
	}
	return n > 0 && len(bytes.TrimSpace(rest)) == 0
}
