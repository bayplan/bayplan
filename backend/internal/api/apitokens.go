package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/session"
)

// Personal API tokens (/api/auth/tokens). Only with a COOKIE session: a token
// cannot create or revoke tokens, so that a leaked one neither becomes
// permanent nor multiplies.

const maxTokenTTLDays = 365

func (h *AuthHandler) requireCookieSession(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return nil, false
	}
	if sess.ViaToken {
		http.Error(w, "this action needs a browser session, not an API token", http.StatusForbidden)
		return nil, false
	}
	return sess, true
}

func (h *AuthHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireCookieSession(w, r)
	if !ok {
		return
	}
	list, err := session.ListAPITokens(r.Context(), h.DB, sess.UserID)
	if err != nil {
		h.Logger.Error("tokens.list: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// CreateToken returns the token value ONCE only.
func (h *AuthHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireCookieSession(w, r)
	if !ok {
		return
	}
	var in struct {
		Name          string `json:"name"`
		ReadOnly      bool   `json:"readOnly"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 60 {
		http.Error(w, "name is required (max. 60 characters)", http.StatusBadRequest)
		return
	}
	if in.ExpiresInDays < 0 || in.ExpiresInDays > maxTokenTTLDays {
		http.Error(w, "expiresInDays must be between 0 (no expiry) and 365", http.StatusBadRequest)
		return
	}
	var expires *time.Time
	if in.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		expires = &t
	}
	raw, tok, err := session.CreateAPIToken(r.Context(), h.DB, sess.UserID, in.Name, in.ReadOnly, expires)
	if errors.Is(err, session.ErrTokenDuplicate) {
		http.Error(w, "you already have a token with that name", http.StatusConflict)
		return
	}
	if err != nil {
		if errors.Is(err, session.ErrTooManyTokens) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		h.Logger.Error("tokens.create: failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, ActorUsername: sess.Username, IP: middleware.ClientIP(r), Action: "api_token.create",
		Resource: tok.Name, Result: audit.ResultSuccess, Detail: map[string]any{"prefix": tok.Prefix, "readOnly": tok.ReadOnly, "expiresAt": tok.ExpiresAt},
	})
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "info": tok})
}

func (h *AuthHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireCookieSession(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	tok, err := session.RevokeAPIToken(r.Context(), h.DB, sess.UserID, id)
	if errors.Is(err, session.ErrTokenNotFound) {
		http.Error(w, "token not found or already revoked", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("tokens.revoke: failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, ActorUsername: sess.Username, IP: middleware.ClientIP(r), Action: "api_token.revoke",
		Resource: tok.Name, Result: audit.ResultSuccess, Detail: map[string]any{"prefix": tok.Prefix},
	})
	writeJSON(w, http.StatusOK, tok)
}
