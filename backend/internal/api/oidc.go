package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/auth"
	"github.com/bayplan/bayplan/internal/metrics"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/oidc"
	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/bayplan/bayplan/internal/session"
	"github.com/bayplan/bayplan/internal/settings"
)

// OIDCHandler implements SSO login with any OpenID Connect provider:
// Authorization Code + PKCE, ID token verification, group resolution
// (configurable claim or fallback to Microsoft Graph) and their mapping to an
// internal role. It resolves to the same session and the same rbac model as
// the local login.
//
// Unlike the old EntraHandler, the provider is not fixed: it is read from the
// oidc.Manager on every request, which rebuilds it when the admin changes the
// configuration in Settings. With SSO disabled the routes respond 404.
type OIDCHandler struct {
	DB           *pgxpool.Pool
	Sessions     *session.Manager
	Logger       *slog.Logger
	Manager      *oidc.Manager
	Graph        *http.Client
	GroupCache   *oidc.GroupCache
	StateSigner  *oidc.StateSigner
	CookieSecure bool
	Metrics      *metrics.Metrics
	Settings     *settings.Store
}

// Login starts the flow: generates state+PKCE, stores them in a short-lived
// signed cookie and redirects to the provider. Public (there is no prior
// session).
func (h *OIDCHandler) Login(w http.ResponseWriter, r *http.Request) {
	provider, ok := h.Manager.Current()
	if !ok {
		http.Error(w, "SSO not configured", http.StatusNotFound)
		return
	}
	state, challenge, err := h.StateSigner.Stash(w, h.CookieSecure)
	if err != nil {
		h.Logger.Error("oidc: failed generating state/PKCE", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, provider.AuthCodeURL(state, challenge), http.StatusFound)
}

// Callback receives the return from the provider, completes the code exchange,
// resolves the user's role and opens a session. Any failure redirects to
// /login?error=... with a specific reason instead of showing a raw error.
func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := middleware.ClientIP(r)

	// The state cookie is single-use, so it is cleared on every way out. Not
	// with a defer: a Set-Cookie added after http.Redirect/http.Error has
	// written the header is silently dropped.
	provider, ok := h.Manager.Current()
	if !ok {
		h.StateSigner.Clear(w, h.CookieSecure)
		http.Error(w, "SSO not configured", http.StatusNotFound)
		return
	}

	fail := func(reason string, logMsg string, err error) {
		if err != nil {
			h.Logger.Error("oidc: "+logMsg, "error", err)
		} else {
			h.Logger.Warn("oidc: " + logMsg)
		}
		h.Metrics.RecordLogin("failure")
		h.StateSigner.Clear(w, h.CookieSecure)
		http.Redirect(w, r, "/login?error="+reason, http.StatusFound)
	}

	if idpErr := r.URL.Query().Get("error"); idpErr != "" {
		fail("sso_failed", "the provider returned an authorization error: "+idpErr, nil)
		return
	}

	wantState, verifier, err := h.StateSigner.Recover(r)
	if err != nil {
		fail("sso_failed", "invalid or expired state cookie", err)
		return
	}
	if r.URL.Query().Get("state") != wantState {
		fail("sso_failed", "returned state does not match (possible CSRF)", nil)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		fail("sso_failed", "callback without code parameter", nil)
		return
	}

	tok, err := provider.Exchange(ctx, code, verifier)
	if err != nil {
		fail("sso_failed", "failed exchanging authorization code", err)
		return
	}

	rawIDToken, _ := tok.Extra("id_token").(string)
	if rawIDToken == "" {
		fail("sso_failed", "token response without id_token", nil)
		return
	}

	claims, err := provider.ParseClaims(ctx, rawIDToken)
	if err != nil {
		fail("sso_failed", "failed verifying id_token", err)
		return
	}

	groups, err := h.resolveGroups(ctx, claims, tok.AccessToken)
	if err != nil {
		fail("sso_failed", "failed resolving the user's groups", err)
		return
	}

	// Same rule as a local username: the claim is IdP input, and the name ends
	// up in the Impersonate-User header, the logs and the audit CSV.
	if err := auth.ValidateUsername(claims.Username); err != nil {
		audit.Log(ctx, h.DB, h.Logger, audit.Entry{
			IP: ip, Action: "login.oidc", Result: audit.ResultFailure,
			Detail: map[string]any{"subject": claims.Subject, "reason": "invalid username claim"},
		})
		fail("invalid_username", "the username claim is empty, too long or has control characters", nil)
		return
	}

	// The user is materialized first and the role is resolved AFTERWARDS: an SSO
	// user may hold a role by group (group_role_mappings) or assigned by hand by
	// an admin (role_assignments), and the latter requires the users row to
	// exist. That way a provider that does not emit groups (Google) is still
	// usable: the admin grants the role after the first login attempt.
	userID, disabled, err := upsertSSOUser(ctx, h.DB, claims)
	if err != nil {
		if errors.Is(err, errUsernameConflict) {
			audit.Log(ctx, h.DB, h.Logger, audit.Entry{
				IP: ip, Action: "login.oidc", Result: audit.ResultFailure,
				Detail: map[string]any{"subject": claims.Subject, "username": claims.Username},
			})
			fail("username_conflict", "the username from the provider is already taken by another user (local, or SSO with a different subject)", err)
			return
		}
		fail("sso_failed", "failed creating/updating user", err)
		return
	}
	if disabled {
		audit.Log(ctx, h.DB, h.Logger, audit.Entry{
			UserID: &userID, IP: ip, Action: "login.oidc", Result: audit.ResultDenied,
			Detail: map[string]any{"reason": "account disabled"},
		})
		fail("account_disabled", "user disabled", nil)
		return
	}

	// The user's 'sso' assignments are replaced with what their groups map to
	// TODAY, also when that is nothing: leaving the IdP group has to take the
	// role away. Manual assignments are not touched (rbac.SyncSSORoleAssignments).
	grants, err := rbac.GrantsFromGroups(ctx, h.DB, groups)
	if err != nil {
		fail("sso_failed", "failed querying group_role_mappings", err)
		return
	}
	keptLastAdmin, err := rbac.SyncSSORoleAssignments(ctx, h.DB, userID, grants)
	if err != nil {
		fail("sso_failed", "failed syncing resolved roles", err)
		return
	}
	if keptLastAdmin {
		// The login goes ahead with the admin role their groups no longer give,
		// because they are the last global admin: it has to be left in the
		// audit log.
		audit.Log(ctx, h.DB, h.Logger, audit.Entry{
			UserID: &userID, IP: ip, Action: "rbac.sync", Result: audit.ResultDenied,
			Detail: map[string]any{"reason": "last_admin", "appliedRole": string(rbac.RoleAdmin), "grants": len(grants)},
		})
	}
	// With no mapped group the login is still fine if an admin granted a role
	// by hand (providers without groups), or a cluster-scoped mapping exists.
	hasAny, err := rbac.HasAnyAssignment(ctx, h.DB, userID)
	if err != nil {
		fail("sso_failed", "failed querying assignments", err)
		return
	}
	if !hasAny {
		audit.Log(ctx, h.DB, h.Logger, audit.Entry{
			UserID: &userID, IP: ip, Action: "login.oidc", Result: audit.ResultDenied,
			Detail: map[string]any{"subject": claims.Subject, "reason": "no mapped group and no assigned role"},
		})
		fail("no_role_mapped", "none of the user's groups maps to a role and there is no manual assignment", nil)
		return
	}

	if err := h.Sessions.Create(ctx, w, r, userID); err != nil {
		fail("sso_failed", "failed creating session", err)
		return
	}

	audit.Log(ctx, h.DB, h.Logger, audit.Entry{
		UserID: &userID, IP: ip, Action: "login.oidc", Result: audit.ResultSuccess,
	})
	h.Metrics.RecordLogin("success")

	h.StateSigner.Clear(w, h.CookieSecure)
	http.Redirect(w, r, "/", http.StatusFound)
}

// resolveGroups returns the user's groups: the token claim if it fits, or the
// Microsoft Graph fallback (with cache) if the token signals overage and the
// admin has enabled it.
func (h *OIDCHandler) resolveGroups(ctx context.Context, claims oidc.IDClaims, accessToken string) ([]string, error) {
	if !claims.GroupsOverflowed {
		return claims.Groups, nil
	}
	if !h.Manager.GraphFallbackEnabled() {
		return nil, fmt.Errorf("the token does not include the groups (overage) and the Microsoft Graph fallback is disabled")
	}
	if cached, ok := h.GroupCache.Get(claims.Subject); ok {
		return cached, nil
	}
	groups, err := oidc.FetchMemberGroups(ctx, h.Graph, accessToken)
	if err != nil {
		return nil, err
	}
	h.GroupCache.Set(claims.Subject, groups)
	return groups, nil
}

var errUsernameConflict = errors.New("oidc: username already in use by another user")

// upsertSSOUser creates the user on their first login or updates them on later
// ones, indexed by sso_subject (never by email or username, which can change).
// Returns errUsernameConflict if the username derived from the token collides
// with an existing user of another origin (e.g. a local one with the same
// name). Name and email are refreshed on EVERY login: the IdP is the source of
// truth for an SSO user (which is why PATCH /api/auth/profile rejects them for
// source='oidc'). They are stored as NULL when the token does not carry them.
func upsertSSOUser(ctx context.Context, pool *pgxpool.Pool, claims oidc.IDClaims) (userID int64, disabled bool, err error) {
	err = pool.QueryRow(ctx,
		`INSERT INTO users (username, source, sso_subject, display_name, email)
		 VALUES ($1, 'oidc', $2, NULLIF($3, ''), NULLIF($4, ''))
		 ON CONFLICT (sso_subject) DO UPDATE SET
		     updated_at = now(),
		     display_name = NULLIF($3, ''),
		     email = NULLIF($4, '')
		 RETURNING id, disabled`,
		claims.Username, claims.Subject, claims.Name, claims.Email,
	).Scan(&userID, &disabled)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return 0, false, fmt.Errorf("%w: %s", errUsernameConflict, claims.Username)
		}
		return 0, false, fmt.Errorf("oidc: user upsert failed: %w", err)
	}
	return userID, disabled, nil
}

// --- Configuration from Settings (implements OIDCSettingsSource) ---

// oidcPublic is the section as the admin sees it: without the secret, with the
// redirect URI already computed and the last provider build error.
type oidcPublic struct {
	Enabled             bool   `json:"enabled"`
	DisplayName         string `json:"displayName"`
	Issuer              string `json:"issuer"`
	ClientID            string `json:"clientId"`
	ClientSecretSet     bool   `json:"clientSecretSet"`
	Scopes              string `json:"scopes"`
	GroupsClaim         string `json:"groupsClaim"`
	UsernameClaim       string `json:"usernameClaim"`
	GraphGroupsFallback bool   `json:"graphGroupsFallback"`
	RedirectURI         string `json:"redirectURI"`
	// Active says whether the provider is really operational (enabled and built
	// successfully); LastError explains why not, if it is not.
	Active    bool   `json:"active"`
	LastError string `json:"lastError,omitempty"`
}

type oidcUpdate struct {
	Enabled             bool    `json:"enabled"`
	DisplayName         string  `json:"displayName"`
	Issuer              string  `json:"issuer"`
	ClientID            string  `json:"clientId"`
	ClientSecret        *string `json:"clientSecret"`
	Scopes              string  `json:"scopes"`
	GroupsClaim         string  `json:"groupsClaim"`
	UsernameClaim       string  `json:"usernameClaim"`
	GraphGroupsFallback bool    `json:"graphGroupsFallback"`
	RedirectURI         string  `json:"redirectURI"`
}

func (h *OIDCHandler) Public(r *http.Request) (any, error) {
	var s oidc.Settings
	if err := h.Settings.Get(r.Context(), settings.KeyOIDC, &s); err != nil {
		return nil, err
	}
	var srv settings.Server
	if err := h.Settings.Get(r.Context(), settings.KeyServer, &srv); err != nil {
		return nil, err
	}
	_, lastErr := h.Manager.Settings()
	out := oidcPublic{
		Enabled: s.Enabled, DisplayName: s.DisplayName, Issuer: s.Issuer, ClientID: s.ClientID,
		ClientSecretSet: s.ClientSecret.IsSet(), Scopes: s.Scopes, GroupsClaim: s.GroupsClaim,
		UsernameClaim: s.UsernameClaim, GraphGroupsFallback: s.GraphGroupsFallback,
		RedirectURI: s.EffectiveRedirectURI(srv.PublicURL), Active: h.Manager.Enabled(),
	}
	if lastErr != nil {
		out.LastError = lastErr.Error()
	}
	return out, nil
}

// Apply validates and saves the configuration. With enabled=true it requires
// issuer, client id and a secret (new or already stored), and checks that the
// issuer answers discovery BEFORE persisting: a typo in the URL must not leave
// the SSO button visible and broken.
func (h *OIDCHandler) Apply(r *http.Request, body json.RawMessage, actor string) (any, error) {
	var in oidcUpdate
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, &ValidationError{Msg: "invalid request body"}
	}
	var current oidc.Settings
	if err := h.Settings.Get(r.Context(), settings.KeyOIDC, &current); err != nil {
		return nil, err
	}

	next := oidc.Settings{
		Enabled:             in.Enabled,
		DisplayName:         strings.TrimSpace(in.DisplayName),
		Issuer:              strings.TrimRight(strings.TrimSpace(in.Issuer), "/"),
		ClientID:            strings.TrimSpace(in.ClientID),
		ClientSecret:        current.ClientSecret,
		Scopes:              strings.Join(strings.Fields(in.Scopes), " "),
		GroupsClaim:         strings.TrimSpace(in.GroupsClaim),
		UsernameClaim:       strings.TrimSpace(in.UsernameClaim),
		GraphGroupsFallback: in.GraphGroupsFallback,
		RedirectURI:         strings.TrimSpace(in.RedirectURI),
	}
	if in.ClientSecret != nil && *in.ClientSecret != "" {
		sec, err := h.Settings.EncryptSecret(*in.ClientSecret)
		if err != nil {
			return nil, err
		}
		next.ClientSecret = sec
	}
	if next.RedirectURI != "" {
		u, err := url.Parse(next.RedirectURI)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, &ValidationError{Msg: "redirectURI must be an absolute http(s) URL"}
		}
	}
	if next.Enabled {
		if next.Issuer == "" || next.ClientID == "" {
			return nil, &ValidationError{Msg: "issuer and clientId are required to enable SSO"}
		}
		if !next.ClientSecret.IsSet() {
			return nil, &ValidationError{Msg: "a client secret is required to enable SSO"}
		}
		if u, err := url.Parse(next.Issuer); err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, &ValidationError{Msg: "issuer must be an absolute https URL"}
		}
		var srv settings.Server
		_ = h.Settings.Get(r.Context(), settings.KeyServer, &srv)
		if next.EffectiveRedirectURI(srv.PublicURL) == "" {
			return nil, &ValidationError{Msg: "configure the server's public URL (or an explicit redirectURI) before enabling SSO"}
		}
		dctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := oidc.Discover(dctx, next.Issuer); err != nil {
			return nil, &ValidationError{Msg: err.Error()}
		}
	}

	if err := h.Settings.Set(r.Context(), settings.KeyOIDC, next, actor); err != nil {
		return nil, err
	}
	// Set has already triggered Manager.Reload through the subscription; Public
	// reflects the result (Active/LastError) so the admin sees it without
	// reloading.
	return h.Public(r)
}
