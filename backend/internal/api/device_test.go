package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bayplan/bayplan/internal/crypto"
)

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	k, err := crypto.NewKeyring("v1", map[crypto.KeyID]string{"v1": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestPasswordEpoch(t *testing.T) {
	if PasswordEpoch("$2a$10$a") == PasswordEpoch("$2a$10$b") || len(PasswordEpoch("x")) != 16 {
		t.Fatal("the epoch must change with the hash and be short")
	}
}

func TestDeviceCookie(t *testing.T) {
	k := testKeyring(t)
	now := time.Unix(1_700_000_000, 0)
	rec := httptest.NewRecorder()
	issueDeviceCookie(rec, k, true, "Admin", "epoch1", now)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected cookie: %+v", cookies)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.AddCookie(cookies[0])

	nonce := knownDevice(req, k, "admin", "epoch1", now)
	if len(nonce) != 32 {
		t.Fatalf("the cookie must vouch for its user (case-insensitive) and carry its nonce, got %q", nonce)
	}
	if knownDevice(req, k, "other", "epoch1", now) != "" {
		t.Fatal("the cookie of one user must not vouch for another")
	}
	if knownDevice(req, k, "admin", "epoch2", now) != "" {
		t.Fatal("a password change (new epoch) must retire the cookie")
	}
	if knownDevice(req, k, "admin", "", now) != "" {
		t.Fatal("no epoch (unknown or disabled user) means no cookie")
	}
	if knownDevice(req, k, "admin", "epoch1", now.Add(deviceCookieTTL+time.Second)) != "" {
		t.Fatal("an expired cookie must not count")
	}
	rec2 := httptest.NewRecorder()
	issueDeviceCookie(rec2, k, true, "admin", "epoch1", now)
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req2.AddCookie(rec2.Result().Cookies()[0])
	if knownDevice(req2, k, "admin", "epoch1", now) == nonce {
		t.Fatal("each cookie needs its own nonce (its own failure bucket)")
	}

	// A keyring ciphertext made for something else does not count, even with
	// the right JSON inside.
	keyID, ct, _ := k.Encrypt([]byte(`{"u":"admin","e":9999999999}`))
	other := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	other.AddCookie(&http.Cookie{Name: deviceCookieName, Value: string(keyID) + "." + base64.RawURLEncoding.EncodeToString(ct)})
	if knownDevice(other, k, "admin", "epoch1", now) != "" {
		t.Fatal("a ciphertext without the device context must not count")
	}

	forged := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	forged.AddCookie(&http.Cookie{Name: deviceCookieName, Value: cookies[0].Value[:len(cookies[0].Value)-2] + "AA"})
	if knownDevice(forged, k, "admin", "epoch1", now) != "" {
		t.Fatal("a tampered cookie must not count")
	}
}
