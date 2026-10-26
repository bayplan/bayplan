package settings

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bayplan/bayplan/internal/config"
)

// legacyEntraSettings is the shape of the auth.oidc section written when
// migrating. It is duplicated here (instead of importing oidc.Settings) so that
// settings does not depend on oidc, which in turn depends on settings.
type legacyEntraSettings struct {
	Enabled             bool   `json:"enabled"`
	DisplayName         string `json:"displayName"`
	Issuer              string `json:"issuer"`
	ClientID            string `json:"clientId"`
	ClientSecret        Secret `json:"clientSecret"`
	Scopes              string `json:"scopes"`
	GroupsClaim         string `json:"groupsClaim"`
	UsernameClaim       string `json:"usernameClaim"`
	GraphGroupsFallback bool   `json:"graphGroupsFallback"`
	RedirectURI         string `json:"redirectURI,omitempty"`
}

// MigrateLegacyEntra converts the AZURE_* variables of earlier deployments
// into an auth.oidc section, exactly ONCE: if the section already exists, the
// environment is ignored (the DB wins, editable from the UI). This way an
// instance that was configured through the environment keeps working without
// touching anything after upgrading, and the next time someone edits the SSO
// they will do it from Settings.
func MigrateLegacyEntra(ctx context.Context, store *Store, cfg *config.Config, logger *slog.Logger) error {
	if !cfg.EntraEnabled() {
		return nil
	}
	var existing legacyEntraSettings
	if err := store.Get(ctx, KeyOIDC, &existing); err != nil {
		return err
	}
	if existing.Issuer != "" {
		logger.Warn("config: the AZURE_* variables are ignored because SSO is already configured in Settings; you can remove them from the environment")
		return nil
	}
	secret, err := store.EncryptSecret(cfg.AzureClientSecret)
	if err != nil {
		return fmt.Errorf("settings: failed to encrypt AZURE_CLIENT_SECRET: %w", err)
	}
	s := legacyEntraSettings{
		Enabled:      true,
		DisplayName:  "Microsoft Entra ID",
		Issuer:       "https://login.microsoftonline.com/" + cfg.AzureTenantID + "/v2.0",
		ClientID:     cfg.AzureClientID,
		ClientSecret: secret,
		// The Graph scope is the one the entra package carried: without it the
		// overage fallback cannot call /me/memberOf.
		Scopes:              "openid profile email https://graph.microsoft.com/GroupMember.Read.All",
		GroupsClaim:         "groups",
		UsernameClaim:       "preferred_username",
		GraphGroupsFallback: true,
		// The redirect URI registered in Azure is kept as is (path
		// /api/auth/entra/callback, which is still served as an alias).
		RedirectURI: cfg.AzureRedirectURI,
	}
	if err := store.Set(ctx, KeyOIDC, s, "AZURE_* migration"); err != nil {
		return err
	}
	logger.Warn("config: SSO migrated from the AZURE_* variables to Settings (settings.auth.oidc); you can now remove them from the environment")
	return nil
}

// SeedServerDefaults fills server.publicURL and server.agentImage from the
// environment (PUBLIC_URL, AGENT_IMAGE) ONLY if they are empty in the DB: it
// is the initial value set by the deployment (chart, compose); whatever the
// admin changes later in Settings prevails on subsequent startups.
func SeedServerDefaults(ctx context.Context, store *Store, cfg *config.Config, logger *slog.Logger) error {
	if cfg.PublicURL == "" && cfg.AgentImage == "" {
		return nil
	}
	var srv Server
	if err := store.Get(ctx, KeyServer, &srv); err != nil {
		return err
	}
	changed := false
	if srv.PublicURL == "" && cfg.PublicURL != "" {
		srv.PublicURL = cfg.PublicURL
		changed = true
	}
	if srv.AgentImage == "" && cfg.AgentImage != "" {
		srv.AgentImage = cfg.AgentImage
		changed = true
	}
	if !changed {
		return nil
	}
	logger.Info("settings: initial server values taken from the environment", "publicURL", srv.PublicURL, "agentImage", srv.AgentImage)
	return store.Set(ctx, KeyServer, srv, "environment")
}
