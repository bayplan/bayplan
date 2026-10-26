package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	josejwt "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// mockOIDCServer brings up a minimal OIDC provider (discovery + JWKS) to test
// New/ParseClaims without depending on a real IdP. It does not implement the
// full token endpoint (that would mainly exercise golang.org/x/oauth2, already
// tested upstream): the focus is our own part — discovery, signature/audience/
// issuer verification and claim extraction with configurable names.
type mockOIDCServer struct {
	*httptest.Server
	key    *rsa.PrivateKey
	keyID  string
	issuer string
}

func newMockOIDCServer(t *testing.T) *mockOIDCServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	m := &mockOIDCServer{key: key, keyID: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                m.issuer,
			"authorization_endpoint":                m.issuer + "/authorize",
			"token_endpoint":                        m.issuer + "/token",
			"jwks_uri":                              m.issuer + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		jwks := josejwt.JSONWebKeySet{Keys: []josejwt.JSONWebKey{{Key: &key.PublicKey, KeyID: m.keyID, Algorithm: "RS256", Use: "sig"}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	m.Server = httptest.NewServer(mux)
	m.issuer = m.Server.URL
	return m
}

func (m *mockOIDCServer) signIDToken(t *testing.T, clientID, subject string, extra map[string]any) string {
	t.Helper()
	signer, err := josejwt.NewSigner(josejwt.SigningKey{Algorithm: josejwt.RS256, Key: m.key}, (&josejwt.SignerOptions{}).WithHeader("kid", m.keyID))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	claims := jwt.Claims{
		Issuer: m.issuer, Subject: subject, Audience: jwt.Audience{clientID},
		Expiry: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)), IssuedAt: jwt.NewNumericDate(time.Now()),
	}
	tok, err := jwt.Signed(signer).Claims(claims).Claims(extra).Serialize()
	if err != nil {
		t.Fatalf("failed signing test ID token: %v", err)
	}
	return tok
}

func TestProviderDiscoveryAndParseClaims(t *testing.T) {
	mock := newMockOIDCServer(t)
	defer mock.Close()

	const clientID = "test-client-id"
	p, err := New(context.Background(), Config{
		Issuer: mock.issuer, ClientID: clientID, ClientSecret: "s", RedirectURL: "https://panel.example/api/auth/oidc/callback",
		Scopes: []string{"profile", "email"}, UsernameClaim: "upn", GroupsClaim: "roles",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.OAuth2.Endpoint.AuthURL == "" || p.OAuth2.Endpoint.TokenURL == "" {
		t.Fatal("discovery did not fill in the endpoints")
	}
	if p.OAuth2.Scopes[0] != "openid" {
		t.Fatalf("openid must be added if missing: %v", p.OAuth2.Scopes)
	}

	raw := mock.signIDToken(t, clientID, "sub-123", map[string]any{"upn": "ana", "roles": []string{"a", "b"}, "name": "Ana"})
	claims, err := p.ParseClaims(context.Background(), raw)
	if err != nil {
		t.Fatalf("ParseClaims: %v", err)
	}
	if claims.Subject != "sub-123" || claims.Username != "ana" || claims.Name != "Ana" || len(claims.Groups) != 2 {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestProviderRejectsWrongAudienceAndIssuer(t *testing.T) {
	mock := newMockOIDCServer(t)
	defer mock.Close()

	p, err := New(context.Background(), Config{Issuer: mock.issuer, ClientID: "expected", ClientSecret: "s"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.ParseClaims(context.Background(), mock.signIDToken(t, "other-client", "s", nil)); err == nil {
		t.Fatal("expected rejection for wrong audience")
	}

	other := newMockOIDCServer(t)
	defer other.Close()
	if _, err := p.ParseClaims(context.Background(), other.signIDToken(t, "expected", "s", nil)); err == nil {
		t.Fatal("expected rejection for another provider's issuer/signature")
	}
}

func TestDiscoverRejectsUnknownIssuer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Discover(ctx, "http://127.0.0.1:1/nothing"); err == nil {
		t.Fatal("expected an error with an unreachable issuer")
	}
}

func TestIsMicrosoftIssuer(t *testing.T) {
	if !IsMicrosoftIssuer("https://login.microsoftonline.com/abc/v2.0") || IsMicrosoftIssuer("https://keycloak.example/realms/x") {
		t.Fatal("wrong Microsoft issuer detection")
	}
}

func TestSettingsEffectiveRedirectURI(t *testing.T) {
	s := Settings{}
	if got := s.EffectiveRedirectURI("https://panel.example/"); got != "https://panel.example/api/auth/oidc/callback" {
		t.Fatalf("got %q", got)
	}
	if got := s.EffectiveRedirectURI(""); got != "" {
		t.Fatalf("without a public URL it must stay empty, got %q", got)
	}
	s.RedirectURI = "https://old.example/api/auth/entra/callback"
	if got := s.EffectiveRedirectURI("https://panel.example"); got != s.RedirectURI {
		t.Fatalf("the explicit one must win, got %q", got)
	}
}
