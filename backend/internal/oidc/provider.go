// Package oidc implements SSO login with any OpenID Connect provider (Entra ID,
// Keycloak, Okta, Google…): Authorization Code + PKCE, ID token verification,
// identity and group extraction with configurable claim names, and the
// Microsoft Graph fallback when Entra ID cannot fit the groups in the token. It
// replaces the old entra package, which only supported a Microsoft tenant fixed
// through environment variables.
//
// It ends in the same place as local login: a session created with
// session.Manager.Create and a role resolved via rbac.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config is what is needed to build a Provider. It comes from settings (see
// Manager), not from the environment.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	// UsernameClaim is the claim used as users.username. Empty =
	// preferred_username, falling back to email and finally to the subject.
	UsernameClaim string
	// GroupsClaim is the claim carrying the group list. Empty = the provider
	// emits no groups and only users with a manually assigned role get in.
	GroupsClaim string
}

// Provider bundles a provider's OAuth2 client and ID token verifier. OIDC
// discovery makes a network call, so it is built once (per configuration) and
// reused on every login.
type Provider struct {
	OAuth2   oauth2.Config
	Verifier *gooidc.IDTokenVerifier
	cfg      Config
}

// New discovers the issuer's configuration and prepares the OAuth2 client for
// Authorization Code + PKCE.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	issuer := strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")
	oidcProvider, err := gooidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: failed discovering configuration of %q: %w", issuer, err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}
	if !containsScope(scopes, gooidc.ScopeOpenID) {
		scopes = append([]string{gooidc.ScopeOpenID}, scopes...)
	}

	oauth2Cfg := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     oidcProvider.Endpoint(),
		Scopes:       scopes,
	}
	verifier := oidcProvider.Verifier(&gooidc.Config{ClientID: cfg.ClientID})

	return &Provider{OAuth2: oauth2Cfg, Verifier: verifier, cfg: cfg}, nil
}

// Discover only checks that the issuer publishes its discovery document. The
// Settings validation uses it to reject a misspelled issuer before saving it,
// without needing the client secret.
func Discover(ctx context.Context, issuer string) error {
	_, err := gooidc.NewProvider(ctx, strings.TrimRight(strings.TrimSpace(issuer), "/"))
	if err != nil {
		return fmt.Errorf("oidc: issuer %q does not answer OIDC discovery: %w", issuer, err)
	}
	return nil
}

func containsScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// AuthCodeURL builds the authorization URL with PKCE S256.
func (p *Provider) AuthCodeURL(state, codeChallenge string) string {
	return p.OAuth2.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// Exchange swaps the authorization code for tokens, including the PKCE
// code_verifier.
func (p *Provider) Exchange(ctx context.Context, code, codeVerifier string) (*oauth2.Token, error) {
	return p.OAuth2.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
}

// IsMicrosoftIssuer says whether the issuer is Entra ID, the only provider for
// which the group fallback via Microsoft Graph makes sense.
func IsMicrosoftIssuer(issuer string) bool {
	return strings.Contains(issuer, "login.microsoftonline.com") || strings.Contains(issuer, "sts.windows.net")
}

// ParseClaims verifies the ID token and extracts the identity according to the
// configured claim names. It is the only place that knows the token's shape.
func (p *Provider) ParseClaims(ctx context.Context, rawIDToken string) (IDClaims, error) {
	idToken, err := p.Verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return IDClaims{}, fmt.Errorf("oidc: failed verifying id_token: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := idToken.Claims(&raw); err != nil {
		return IDClaims{}, fmt.Errorf("oidc: failed decoding claims: %w", err)
	}
	return ExtractClaims(raw, idToken.Subject, p.cfg.UsernameClaim, p.cfg.GroupsClaim), nil
}
