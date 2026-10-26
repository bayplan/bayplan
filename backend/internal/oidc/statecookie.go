package oidc

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// stateCookieName is the cookie carrying state+PKCE verifier between
// login-start and the callback. Short-lived (~5 min): the user must complete
// the login at the IdP within that window.
const stateCookieName = "bayplan_oidc_state"
const stateCookieTTL = 5 * time.Minute

var ErrInvalidStateCookie = errors.New("oidc: invalid, expired or tampered state cookie")

// StateSigner signs the state+verifier value with HMAC so the cookie can be
// neither forged nor tampered with in transit. The key is random and lives
// only in process memory: if the backend restarts mid-login, the user simply
// tries again (~5 min window, consistent with the single-instance deployment
// already decided for the project). It does not reuse crypto.Keyring: that key
// protects secrets at rest (ServiceAccount tokens); this is an ephemeral
// signature of a value that lives for seconds.
type StateSigner struct {
	key []byte
	now func() time.Time // injectable for the expiry test
}

// stateCookiePath must cover both the callback and its legacy alias
// (/api/auth/oidc/callback and /api/auth/entra/callback): with the old
// "/api/auth/oidc" the browser never sent the cookie to the Entra alias and
// every login through it failed with an invalid state.
const stateCookiePath = "/api/auth"

// legacyStateCookiePath is where versions before the fix set the cookie; Clear
// also expires it so that a leftover one does not shadow the new one (the
// browser sends the more specific path first).
const legacyStateCookiePath = "/api/auth/oidc"

func NewStateSigner() (*StateSigner, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("oidc: failed generating state signing key: %w", err)
	}
	return &StateSigner{key: key, now: time.Now}, nil
}

// Stash generates state+PKCE, signs them and stores them in a short-lived
// httpOnly cookie, returning the state and the code_challenge to build the
// authorization URL.
func (s *StateSigner) Stash(w http.ResponseWriter, secure bool) (state, challenge string, err error) {
	state, err = newState()
	if err != nil {
		return "", "", err
	}
	pkce, err := newPKCE()
	if err != nil {
		return "", "", err
	}

	// The issue time goes inside the signed payload: the cookie's MaxAge is
	// only a hint to the browser, and a copied cookie value would otherwise be
	// valid for as long as the process lives.
	payload := state + "." + pkce.Verifier + "." + strconv.FormatInt(s.now().Unix(), 10)
	sig := s.sign(payload)
	value := payload + "." + sig

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    value,
		Path:     stateCookiePath,
		MaxAge:   int(stateCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode, // must survive the top-level redirect back from the IdP
	})

	return state, pkce.Challenge, nil
}

// Recover validates and extracts state+verifier from the request cookie.
func (s *StateSigner) Recover(r *http.Request) (state, verifier string, err error) {
	cookie, err := r.Cookie(stateCookieName)
	if err != nil || cookie.Value == "" {
		return "", "", ErrInvalidStateCookie
	}

	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 4 {
		return "", "", ErrInvalidStateCookie
	}
	state, verifier, issued, sig := parts[0], parts[1], parts[2], parts[3]

	expected := s.sign(state + "." + verifier + "." + issued)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", "", ErrInvalidStateCookie
	}
	iat, err := strconv.ParseInt(issued, 10, 64)
	if err != nil {
		return "", "", ErrInvalidStateCookie
	}
	age := s.now().Sub(time.Unix(iat, 0))
	if age > stateCookieTTL || age < -time.Minute { // small tolerance, same clock
		return "", "", ErrInvalidStateCookie
	}

	return state, verifier, nil
}

// Clear deletes the state cookie (after consuming it in the callback, success
// or not). Must be called BEFORE writing the response.
func (s *StateSigner) Clear(w http.ResponseWriter, secure bool) {
	for _, path := range []string{stateCookiePath, legacyStateCookiePath} {
		http.SetCookie(w, &http.Cookie{
			Name:     stateCookieName,
			Value:    "",
			Path:     path,
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func (s *StateSigner) sign(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
