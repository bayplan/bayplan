package oidc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStateCookieRoundtrip(t *testing.T) {
	signer, err := NewStateSigner()
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}

	rec := httptest.NewRecorder()
	state, challenge, err := signer.Stash(rec, false)
	if err != nil {
		t.Fatalf("Stash: %v", err)
	}
	if state == "" || challenge == "" {
		t.Fatal("Stash returned empty state or challenge")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	gotState, gotVerifier, err := signer.Recover(req)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if gotState != state {
		t.Errorf("recovered state = %q, want %q", gotState, state)
	}
	if gotVerifier == "" {
		t.Error("recovered verifier is empty")
	}
}

func TestStateCookieRejectsTampering(t *testing.T) {
	signer, err := NewStateSigner()
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}

	rec := httptest.NewRecorder()
	if _, _, err := signer.Stash(rec, false); err != nil {
		t.Fatalf("Stash: %v", err)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	tampered := cookies[0]
	tampered.Value = tampered.Value + "x" // tamper with the signature

	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback", nil)
	req.AddCookie(tampered)

	if _, _, err := signer.Recover(req); err == nil {
		t.Fatal("expected an error recovering a tampered cookie")
	}
}

func TestStateCookieRejectsWrongSigner(t *testing.T) {
	signerA, err := NewStateSigner()
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	signerB, err := NewStateSigner()
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}

	rec := httptest.NewRecorder()
	if _, _, err := signerA.Stash(rec, false); err != nil {
		t.Fatalf("Stash: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	// signerB has a different HMAC key (simulates a process restart
	// mid-login): it must reject the cookie signed by signerA.
	if _, _, err := signerB.Recover(req); err == nil {
		t.Fatal("expected an error recovering with a different signing key")
	}
}

func TestRecoverWithoutCookie(t *testing.T) {
	signer, err := NewStateSigner()
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback", nil)
	if _, _, err := signer.Recover(req); err == nil {
		t.Fatal("expected an error without a state cookie")
	}
}

func TestStateCookieExpires(t *testing.T) {
	signer, err := NewStateSigner()
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	start := time.Unix(1_700_000_000, 0)
	signer.now = func() time.Time { return start }
	rec := httptest.NewRecorder()
	if _, _, err := signer.Stash(rec, false); err != nil {
		t.Fatalf("Stash: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	signer.now = func() time.Time { return start.Add(stateCookieTTL - time.Second) }
	if _, _, err := signer.Recover(req); err != nil {
		t.Fatalf("Recover within TTL: %v", err)
	}
	signer.now = func() time.Time { return start.Add(stateCookieTTL + time.Second) }
	if _, _, err := signer.Recover(req); err == nil {
		t.Fatal("expected an error recovering a cookie older than the TTL")
	}
}

// The cookie path must cover the legacy /api/auth/entra/callback alias too.
func TestStateCookiePathCoversEntraAlias(t *testing.T) {
	signer, err := NewStateSigner()
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	rec := httptest.NewRecorder()
	if _, _, err := signer.Stash(rec, false); err != nil {
		t.Fatalf("Stash: %v", err)
	}
	c := rec.Result().Cookies()[0]
	for _, p := range []string{"/api/auth/oidc/callback", "/api/auth/entra/callback"} {
		if !strings.HasPrefix(p, c.Path+"/") {
			t.Errorf("cookie path %q is not sent to %q", c.Path, p)
		}
	}
}
