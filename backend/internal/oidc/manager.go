package oidc

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bayplan/bayplan/internal/settings"
)

// Settings is the auth.oidc section as stored in the settings table. The client
// secret is encrypted with the keyring; it never leaves towards the frontend.
type Settings struct {
	Enabled      bool            `json:"enabled"`
	DisplayName  string          `json:"displayName"`
	Issuer       string          `json:"issuer"`
	ClientID     string          `json:"clientId"`
	ClientSecret settings.Secret `json:"clientSecret"`
	// Space-separated scopes, as in the authorization URL.
	Scopes        string `json:"scopes"`
	GroupsClaim   string `json:"groupsClaim"`
	UsernameClaim string `json:"usernameClaim"`
	// GraphGroupsFallback enables querying Microsoft Graph when the token
	// signals group overage. Only makes sense with Entra ID.
	GraphGroupsFallback bool `json:"graphGroupsFallback"`
	// Explicit RedirectURI. Empty = <server.publicURL>/api/auth/oidc/callback.
	// It exists so as not to break app registrations already created with the
	// old /api/auth/entra/callback path when migrating from the AZURE_*
	// variables.
	RedirectURI string `json:"redirectURI,omitempty"`
}

// DefaultCallbackPath is the callback path registered at the IdP.
const DefaultCallbackPath = "/api/auth/oidc/callback"

// ScopeList splits the scopes string.
func (s Settings) ScopeList() []string {
	return strings.Fields(s.Scopes)
}

// EffectiveRedirectURI resolves the redirect URI with the given public URL.
func (s Settings) EffectiveRedirectURI(publicURL string) string {
	if s.RedirectURI != "" {
		return s.RedirectURI
	}
	if publicURL == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + DefaultCallbackPath
}

// discoveryTimeout bounds the OIDC discovery when (re)building the provider: a
// downed issuer cannot block startup nor a save in Settings.
const discoveryTimeout = 10 * time.Second

// Manager keeps the active Provider and rebuilds it when the configuration in
// settings changes, without restarting the binary. It is the piece that turns
// SSO from "four environment variables and a redeploy" into something the admin
// edits from the UI.
type Manager struct {
	store  *settings.Store
	logger *slog.Logger

	mu       sync.RWMutex
	provider *Provider
	current  Settings
	lastErr  error
}

func NewManager(store *settings.Store, logger *slog.Logger) *Manager {
	m := &Manager{store: store, logger: logger}
	// server.publicURL matters too (redirect URI), so it reloads on either of
	// the two sections.
	store.Subscribe(func(key string) {
		if key == settings.KeyOIDC || key == settings.KeyServer {
			m.Reload(context.Background())
		}
	})
	return m
}

// Reload re-reads the configuration and rebuilds the provider. A failure
// (unreachable issuer, undecryptable secret) leaves SSO disabled and the error
// available to the Settings UI; it never brings the process down.
func (m *Manager) Reload(ctx context.Context) {
	var s Settings
	if err := m.store.Get(ctx, settings.KeyOIDC, &s); err != nil {
		m.setState(nil, s, err)
		return
	}
	if !s.Enabled {
		m.setState(nil, s, nil)
		return
	}
	var srv settings.Server
	_ = m.store.Get(ctx, settings.KeyServer, &srv)

	secret, err := m.store.DecryptSecret(s.ClientSecret)
	if err != nil {
		m.setState(nil, s, err)
		return
	}
	dctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	p, err := New(dctx, Config{
		Issuer:        s.Issuer,
		ClientID:      s.ClientID,
		ClientSecret:  secret,
		RedirectURL:   s.EffectiveRedirectURI(srv.PublicURL),
		Scopes:        s.ScopeList(),
		UsernameClaim: s.UsernameClaim,
		GroupsClaim:   s.GroupsClaim,
	})
	m.setState(p, s, err)
}

func (m *Manager) setState(p *Provider, s Settings, err error) {
	m.mu.Lock()
	m.provider, m.current, m.lastErr = p, s, err
	m.mu.Unlock()
	switch {
	case err != nil:
		m.logger.Error("oidc: provider unavailable", "issuer", s.Issuer, "error", err)
	case p != nil:
		m.logger.Info("oidc: provider ready", "issuer", s.Issuer, "redirectURI", p.OAuth2.RedirectURL)
	default:
		m.logger.Info("oidc: SSO disabled")
	}
}

// Current returns the active provider, or false if SSO is disabled or failed to
// build.
func (m *Manager) Current() (*Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider, m.provider != nil
}

// Enabled says whether there is a working provider (what gates the login button).
func (m *Manager) Enabled() bool {
	_, ok := m.Current()
	return ok
}

// Settings returns the loaded configuration and the last build error.
func (m *Manager) Settings() (Settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current, m.lastErr
}

// GraphFallbackEnabled says whether groups have to be resolved via Microsoft
// Graph when the token signals overage.
func (m *Manager) GraphFallbackEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current.GraphGroupsFallback && IsMicrosoftIssuer(m.current.Issuer)
}
