package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bayplan/bayplan/internal/crypto"
)

// The device cookie marks a browser that has already logged in as a given
// user (OWASP's "device cookies"): the per-account login limiter does not
// apply to it. Without it, anyone could keep the admin locked out just by
// failing on that username; with it, an attacker can only lock out browsers
// that never logged in, and cannot forge one (it is encrypted and
// authenticated with the keyring, so it also survives restarts and key
// rotation like every other secret).
const (
	deviceCookieName = "bayplan_device"
	deviceCookieTTL  = 90 * 24 * time.Hour
)

// deviceContext prefixes the sealed payload: the keyring encrypts other
// secrets too, and a ciphertext produced for anything else must never parse
// as a device cookie, even if its plaintext happened to be this JSON.
const deviceContext = "bayplan-device-cookie-v1\x00"

// devicePayload: Nonce identifies the cookie so its own failed attempts are
// limited (a cookie skips the shared limiters, it must not buy unlimited
// guesses); Epoch is derived from the password hash, so changing the
// password retires every device cookie issued before.
type devicePayload struct {
	User  string `json:"u"`
	Exp   int64  `json:"e"`
	Nonce string `json:"n"`
	Epoch string `json:"p"`
}

// PasswordEpoch derives a short, non-reversible tag from a password hash
// (bcrypt hashes carry a fresh salt, so any change of password changes it).
func PasswordEpoch(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:8])
}

func issueDeviceCookie(w http.ResponseWriter, keyring *crypto.Keyring, secure bool, username, epoch string, now time.Time) {
	if keyring == nil || epoch == "" {
		return
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return
	}
	raw, _ := json.Marshal(devicePayload{User: strings.ToLower(username), Exp: now.Add(deviceCookieTTL).Unix(), Nonce: hex.EncodeToString(nonce), Epoch: epoch})
	keyID, ct, err := keyring.Encrypt(append([]byte(deviceContext), raw...))
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     deviceCookieName,
		Value:    string(keyID) + "." + base64.RawURLEncoding.EncodeToString(ct),
		Path:     "/api/auth",
		MaxAge:   int(deviceCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// knownDevice returns the nonce of a valid, unexpired device cookie for
// username issued under the current password (epoch), or "" if there is none.
func knownDevice(r *http.Request, keyring *crypto.Keyring, username, epoch string, now time.Time) string {
	p, ok := parseDevice(r, keyring)
	if !ok || epoch == "" || p.Epoch != epoch || p.Nonce == "" || p.User != strings.ToLower(username) || now.Unix() >= p.Exp {
		return ""
	}
	return p.Nonce
}

func parseDevice(r *http.Request, keyring *crypto.Keyring) (devicePayload, bool) {
	var p devicePayload
	c, err := r.Cookie(deviceCookieName)
	if err != nil || keyring == nil {
		return p, false
	}
	keyID, enc, ok := strings.Cut(c.Value, ".")
	if !ok {
		return p, false
	}
	ct, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return p, false
	}
	raw, err := keyring.Decrypt(crypto.KeyID(keyID), ct)
	if err != nil {
		return p, false
	}
	payload, ok := strings.CutPrefix(string(raw), deviceContext)
	if !ok {
		return p, false
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return p, false
	}
	return p, true
}
