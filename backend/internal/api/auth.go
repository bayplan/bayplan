package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/bayplan/bayplan/internal/crypto"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/auth"
	"github.com/bayplan/bayplan/internal/metrics"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/oidc"
	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/bayplan/bayplan/internal/session"
)

// AuthHandler groups the local login, session and initial setup endpoints.
// SSO login (OIDC) lives in a separate package/handler (OIDCHandler in
// oidc.go) that resolves to the same session and the same rbac model.
type AuthHandler struct {
	DB           *pgxpool.Pool
	Sessions     *session.Manager
	Logger       *slog.Logger
	LoginLimiter *middleware.IPRateLimiter
	// AccountLimiter counts FAILED logins per username (see Login).
	AccountLimiter *middleware.AccountLimiter
	// Keyring seals the device cookie (device.go).
	Keyring *crypto.Keyring
	SSO     SSOStatus // OIDC provider status, for the login button
	Metrics *metrics.Metrics
}

// maxAuthBody caps the JSON bodies of the auth, user, profile and group
// mapping endpoints: they carry a handful of short fields, and an unbounded
// decoder on a public route (login, setup) is a free memory sink.
const maxAuthBody = 1 << 16

// SSOStatus is what /api/auth/status needs from the OIDC provider.
type SSOStatus interface {
	Enabled() bool
	Settings() (oidc.Settings, error)
}

type userResponse struct {
	// ID is needed by the frontend to know which user in the list is itself:
	// it used not to be exposed and the UI inferred it by comparing the
	// username, which is correct but fragile.
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	// Source distinguishes 'local' from 'oidc': the profile fields of an SSO
	// user are sent by the IdP and the UI must not offer them as editable.
	Source             string `json:"source"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"mustChangePassword"`
	// ViaToken: the request came with an API token (Role already reflects the
	// read-only cap if it has one). Scripts use it to know what they are
	// operating with; the frontend never sees it as true.
	ViaToken bool `json:"viaToken,omitempty"`
	// Preferences is the preferences JSON exactly as stored. The backend does
	// not interpret it: the UI is who knows what "density" means, and this way
	// adding a preference touches neither Go nor the database.
	Preferences json.RawMessage `json:"preferences"`
}

func (h *AuthHandler) currentUserResponse(r *http.Request, userID int64, username string, mustChange bool) (userResponse, error) {
	resp := userResponse{ID: userID, Username: username, MustChangePassword: mustChange}

	var displayName, email *string
	var preferences []byte
	err := h.DB.QueryRow(r.Context(),
		`SELECT display_name, email, source, preferences FROM users WHERE id = $1`, userID,
	).Scan(&displayName, &email, &resp.Source, &preferences)
	if err != nil {
		return userResponse{}, err
	}
	if displayName != nil {
		resp.DisplayName = *displayName
	}
	if email != nil {
		resp.Email = *email
	}
	resp.Preferences = preferences

	role, err := rbac.EffectiveGlobalRole(r.Context(), h.DB, userID)
	if err != nil {
		if errors.Is(err, rbac.ErrNoRole) {
			return resp, nil
		}
		return userResponse{}, err
	}
	resp.Role = string(role)
	return resp, nil
}

// passwordEpoch is the device-cookie epoch of a local, enabled user's
// current password, "" if there is no such user.
func (h *AuthHandler) passwordEpoch(ctx context.Context, username string) (string, error) {
	var hash *string
	err := h.DB.QueryRow(ctx, `SELECT password_hash FROM users WHERE username = $1 AND source = 'local' AND NOT disabled`, username).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && hash == nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return PasswordEpoch(*hash), nil
}

// jsonBody requires Content-Type: application/json on the two routes that
// create a session without one (login, setup): a cross-site <form> can only
// send text/plain or form encodings, so this is what stops a login-CSRF that
// would leave the attacker's session in the victim's browser. The CSRF
// middleware cannot do it, there is no cookie yet.
func jsonBody(w http.ResponseWriter, r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if !strings.EqualFold(strings.TrimSpace(ct), "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// Login authenticates a local username+password and opens a session. Rate
// limited by IP to hinder brute force; every attempt (success or failure) is
// audited.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if !jsonBody(w, r) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	// A browser that already logged in as this user (device cookie) skips
	// both limiters. Behind a proxy that is not in TRUSTED_PROXIES every
	// client shares the proxy's IP, and one attacker emptying that bucket
	// would otherwise lock everybody out.
	// The cookie gets a bucket of its own instead: it must not buy unlimited
	// guesses either.
	accountKey := strings.ToLower(strings.TrimSpace(body.Username))
	epoch, err := h.passwordEpoch(r.Context(), body.Username)
	if err != nil {
		h.Logger.Error("login: failed to read the password epoch", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	device := knownDevice(r, h.Keyring, accountKey, epoch, time.Now())
	if device == "" && h.LoginLimiter != nil && !h.LoginLimiter.Allow(r) {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}
	// Second bucket per account, independent of the IP: X-Forwarded-For is
	// only as trustworthy as TRUSTED_PROXIES, and a user inside those ranges
	// could pick a fresh IP per attempt.
	// A browser that already logged in as this user skips it, so failures by
	// others cannot lock the owner out (device.go).
	bucket := accountKey
	if device != "" {
		bucket = "device:" + device
	}
	reserved := false
	if h.AccountLimiter != nil {
		if !h.AccountLimiter.Acquire(bucket) {
			http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
			return
		}
		reserved = true
	}

	ip := middleware.ClientIP(r)

	user, err := auth.Authenticate(r.Context(), h.DB, body.Username, body.Password)
	if reserved {
		h.AccountLimiter.Release(bucket, err != nil)
	}
	if err != nil {
		audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
			IP: ip, Action: "login.local", Result: audit.ResultFailure,
			Detail: map[string]any{"username": body.Username},
		})
		h.Metrics.RecordLogin("failure")
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if err := h.Sessions.Create(r.Context(), w, r, user.ID); err != nil {
		h.Logger.Error("login: failed to create session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &user.ID, IP: ip, Action: "login.local", Result: audit.ResultSuccess,
	})
	if epoch, err := h.passwordEpoch(r.Context(), user.Username); err == nil {
		issueDeviceCookie(w, h.Keyring, h.Sessions.CookieSecure, accountKey, epoch, time.Now())
	}
	h.Metrics.RecordLogin("success")

	resp, err := h.currentUserResponse(r, user.ID, user.Username, user.MustChangePassword)
	if err != nil {
		h.Logger.Error("login: failed to resolve role", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Me returns the user of the current session.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	resp, err := h.currentUserResponse(r, sess.UserID, sess.Username, sess.MustChangePassword)
	if err != nil {
		h.Logger.Error("me: failed to resolve role", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp.ViaToken = sess.ViaToken
	if resp.Role != "" {
		resp.Role = string(middleware.CapRoleFromContext(r.Context(), rbac.Role(resp.Role)))
	}
	writeJSON(w, http.StatusOK, resp)
}

// Logout revokes the current session and clears the cookies.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	sess, _ := middleware.SessionFromContext(r.Context())
	if err := h.Sessions.Revoke(r.Context(), r); err != nil {
		h.Logger.Error("logout: failed to revoke session", "error", err)
	}
	h.Sessions.ClearCookies(w)
	if sess != nil {
		audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
			UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "logout", Result: audit.ResultSuccess,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// Status exposes whether an initial setup is pending, so the frontend can
// decide whether to show the setup screen instead of the login one.
func (h *AuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	pending, err := auth.SetupPending(r.Context(), h.DB)
	if err != nil {
		h.Logger.Error("status: failed to check pending setup", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp := map[string]any{"setupPending": pending, "ssoEnabled": false}
	if h.SSO != nil && h.SSO.Enabled() {
		resp["ssoEnabled"] = true
		if s, _ := h.SSO.Settings(); s.DisplayName != "" {
			resp["ssoDisplayName"] = s.DisplayName
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// Setup consumes the one-time setup token from the first start and sets the
// admin password, leaving the admin logged in.
func (h *AuthHandler) Setup(w http.ResponseWriter, r *http.Request) {
	if !jsonBody(w, r) {
		return
	}
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	userID, err := auth.CompleteSetup(r.Context(), h.DB, body.Token, body.NewPassword)
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrInvalidOrExpiredToken):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, auth.ErrWeakPassword):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	default:
		// Anything else is a DB failure: its text (wrapped pgx error) is for
		// the log, not for an unauthenticated caller.
		h.Logger.Error("setup: failed to complete setup", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.Sessions.Create(r.Context(), w, r, userID); err != nil {
		h.Logger.Error("setup: failed to create session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &userID, IP: middleware.ClientIP(r), Action: "auth.setup_completed", Result: audit.ResultSuccess,
	})

	// The bootstrap user is "admin" today, but the response must not assume
	// it: read the real name.
	var username string
	if err := h.DB.QueryRow(r.Context(), `SELECT username FROM users WHERE id = $1`, userID).Scan(&username); err != nil {
		h.Logger.Error("setup: failed to read user", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp, err := h.currentUserResponse(r, userID, username, false)
	if err != nil {
		h.Logger.Error("setup: failed to resolve role", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ChangePassword lets an authenticated user change their own password,
// verifying the current one. It revokes the user's other active sessions (the
// current one is re-issued) to force closing sessions open on other devices
// after the change.
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	// Cookie sessions only, like token management: a token (read-only or not)
	// must never turn into a full browser session, and this handler hands one
	// out. The login limiter applies too, otherwise it would be an unthrottled
	// oracle for the current password.
	sess, ok := h.requireCookieSession(w, r)
	if !ok {
		return
	}
	if h.LoginLimiter != nil && !h.LoginLimiter.Allow(r) {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}

	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := auth.ChangePassword(r.Context(), h.DB, sess.UserID, body.CurrentPassword, body.NewPassword); err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			http.Error(w, err.Error(), http.StatusUnauthorized)
		case errors.Is(err, auth.ErrWeakPassword):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		default:
			h.Logger.Error("change-password: failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	if err := session.RevokeAllForUser(r.Context(), h.DB, sess.UserID); err != nil {
		h.Logger.Error("change-password: failed to revoke sessions", "error", err)
	}
	if err := h.Sessions.Create(r.Context(), w, r, sess.UserID); err != nil {
		h.Logger.Error("change-password: failed to create new session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, IP: middleware.ClientIP(r), Action: "auth.password_changed", Result: audit.ResultSuccess,
	})

	resp, err := h.currentUserResponse(r, sess.UserID, sess.Username, false)
	if err != nil {
		h.Logger.Error("change-password: failed to resolve role", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
