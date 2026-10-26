// Package session manages revocable server-side sessions: the token lives as
// an httpOnly cookie in the browser, and its hash (never the plaintext value)
// is stored in Postgres. This allows revoking sessions instantly (disabling a
// user, logout, incident) without waiting for a JWT to expire.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/clientip"
)

const (
	CookieName     = "bayplan_session"
	CSRFCookieName = "bayplan_csrf"
)

var ErrSessionNotFound = errors.New("session: session is invalid, expired or revoked")

// Session is the session resolved from the request cookie.
type Session struct {
	UserID             int64
	Username           string
	MustChangePassword bool
	// ViaToken: authenticated with an API token (Authorization: Bearer), not
	// with the cookie. No CSRF (there is no cookie a third party could make the
	// browser send) and no permission to manage tokens (a token does not create
	// tokens).
	ViaToken bool
	// ReadOnly: the token caps the effective role to readonly everywhere.
	ReadOnly bool
}

// Manager creates and validates sessions. IdleTimeout and AbsoluteTimeout
// come from config.Config (SESSION_IDLE_TIMEOUT / SESSION_ABSOLUTE_TIMEOUT).
type Manager struct {
	Pool            *pgxpool.Pool
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	CookieSecure    bool
}

// Create opens a new session for userID and sets the session cookie
// (httpOnly) and the CSRF cookie (readable by JS, for the double-submit
// pattern) on the response.
func (m *Manager) Create(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int64) error {
	rawToken, tokenHash, err := newRandomToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(m.AbsoluteTimeout)

	if _, err := m.Pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, ip, user_agent, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		tokenHash, userID, clientIP(r), r.UserAgent(), expiresAt,
	); err != nil {
		return fmt.Errorf("session: failed to create session: %w", err)
	}

	csrfToken, _, err := newRandomToken()
	if err != nil {
		return err
	}

	setCookie(w, CookieName, rawToken, m.AbsoluteTimeout, true, m.CookieSecure)
	setCookie(w, CSRFCookieName, csrfToken, m.AbsoluteTimeout, false, m.CookieSecure)
	return nil
}

// Validate resolves the session from the request cookie, checking revocation,
// absolute expiration and inactivity expiration (sliding: every successful
// validation refreshes last_seen_at).
func (m *Manager) Validate(ctx context.Context, r *http.Request) (*Session, error) {
	// An API token in Authorization wins over the cookie: it is what a script
	// uses, and this way a tab open in the same browser does not override it.
	if tok := bearerToken(r); tok != "" {
		return m.validateAPIToken(ctx, tok)
	}
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return nil, ErrSessionNotFound
	}
	tokenHash := hashToken(cookie.Value)

	var (
		s          Session
		expiresAt  time.Time
		lastSeenAt time.Time
		revokedAt  *time.Time
		disabled   bool
	)
	err = m.Pool.QueryRow(ctx,
		`SELECT s.user_id, u.username, u.must_change_password, s.expires_at, s.last_seen_at, s.revoked_at, u.disabled
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1`,
		tokenHash,
	).Scan(&s.UserID, &s.Username, &s.MustChangePassword, &expiresAt, &lastSeenAt, &revokedAt, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("session: failed to query session: %w", err)
	}

	now := time.Now()
	// disabled is checked here, not only at login: disabling a user has to
	// kick them out NOW. UsersHandler.SetDisabled also calls RevokeAllForUser,
	// but an UPDATE users SET disabled = true done with direct SQL does not go
	// through there, and without this check the user would keep operating with
	// their open session until it expired.
	if revokedAt != nil || disabled || now.After(expiresAt) || now.Sub(lastSeenAt) > m.IdleTimeout {
		return nil, ErrSessionNotFound
	}

	// Sliding: every valid use extends the allowed inactivity window (never
	// beyond expires_at, the absolute expiration).
	if _, err := m.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at = now() WHERE token_hash = $1`, tokenHash); err != nil {
		return nil, fmt.Errorf("session: failed to update last_seen_at: %w", err)
	}

	return &s, nil
}

// Revoke invalidates the session tied to the request cookie (logout).
func (m *Manager) Revoke(ctx context.Context, r *http.Request) error {
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	tokenHash := hashToken(cookie.Value)
	_, err = m.Pool.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	if err != nil {
		return fmt.Errorf("session: failed to revoke session: %w", err)
	}
	return nil
}

// RevokeAllForUser revokes every active session of a user (used when
// disabling them or changing their password).
func RevokeAllForUser(ctx context.Context, pool *pgxpool.Pool, userID int64) error {
	_, err := pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("session: failed to revoke the user's sessions: %w", err)
	}
	// API tokens are long-lived sessions: disabling a user or changing their
	// password cuts them off too.
	if err := revokeAPITokensForUser(ctx, pool, userID); err != nil {
		return fmt.Errorf("session: failed to revoke the user's API tokens: %w", err)
	}
	return nil
}

// ClearCookies removes the session and CSRF cookies from the browser (logout).
func (m *Manager) ClearCookies(w http.ResponseWriter) {
	clearCookie(w, CookieName, true, m.CookieSecure)
	clearCookie(w, CSRFCookieName, false, m.CookieSecure)
}

func newRandomToken() (raw string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("session: failed to generate token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration, httpOnly, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string, httpOnly, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clientIP(r *http.Request) string {
	return clientip.FromRequest(r)
}
