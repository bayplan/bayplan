package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Personal API tokens (api_tokens table, migration 0020). They are presented
// in `Authorization: Bearer bpl_…` and resolve to the SAME Session as a
// cookie, with two flags: ViaToken (no CSRF: there is no cookie to steal, and
// it cannot manage tokens) and ReadOnly (caps the role to readonly everywhere).

// TokenPrefix identifies an API token in the Authorization header.
const TokenPrefix = "bpl_"

// ErrTooManyTokens is returned by CreateAPIToken when the user already holds
// MaxTokensPerUser active tokens; the API maps it to 409.
var ErrTooManyTokens = errors.New("maximum number of active tokens reached; revoke one first")

var (
	ErrTokenNotFound  = errors.New("session: invalid API token")
	ErrTokenDuplicate = errors.New("session: a token with that name already exists")
	// MaxTokensPerUser prevents piling up forgotten tokens.
	MaxTokensPerUser = 20
)

// APIToken is an api_tokens row as listed (never the value).
type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	ReadOnly   bool       `json:"readOnly"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// bearerToken returns the API token from the header, or "" if absent or not
// in our format (a foreign Bearer is ignored and the cookie is used instead).
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	if !strings.HasPrefix(tok, TokenPrefix) {
		return ""
	}
	return tok
}

// validateAPIToken resolves the session of an API token.
func (m *Manager) validateAPIToken(ctx context.Context, raw string) (*Session, error) {
	hash := hashToken(raw)
	var (
		s         Session
		id        int64
		expiresAt *time.Time
		revokedAt *time.Time
		disabled  bool
	)
	err := m.Pool.QueryRow(ctx,
		`SELECT t.id, t.user_id, u.username, t.read_only, t.expires_at, t.revoked_at, u.disabled
		 FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = $1`, hash,
	).Scan(&id, &s.UserID, &s.Username, &s.ReadOnly, &expiresAt, &revokedAt, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("session: failed to query API token: %w", err)
	}
	if revokedAt != nil || disabled || (expiresAt != nil && time.Now().After(*expiresAt)) {
		return nil, ErrTokenNotFound
	}
	s.ViaToken = true
	// last_used_at with minute granularity: a script making a hundred
	// requests per second does not have to write a hundred times.
	_, _ = m.Pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id)
	return &s, nil
}

// CreateAPIToken generates a new token for the user and returns the plaintext
// value exactly ONCE; the hash is what stays in the DB.
func CreateAPIToken(ctx context.Context, pool *pgxpool.Pool, userID int64, name string, readOnly bool, expiresAt *time.Time) (raw string, tok APIToken, err error) {
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE user_id = $1 AND revoked_at IS NULL`, userID).Scan(&count); err != nil {
		return "", APIToken{}, err
	}
	if count >= MaxTokensPerUser {
		return "", APIToken{}, fmt.Errorf("%w (limit %d)", ErrTooManyTokens, MaxTokensPerUser)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", APIToken{}, err
	}
	raw = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	prefix := raw[:len(TokenPrefix)+8]
	err = pool.QueryRow(ctx,
		`INSERT INTO api_tokens (user_id, name, token_hash, prefix, read_only, expires_at) VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, name, prefix, read_only, expires_at, last_used_at, revoked_at, created_at`,
		userID, name, hashToken(raw), prefix, readOnly, expiresAt,
	).Scan(&tok.ID, &tok.Name, &tok.Prefix, &tok.ReadOnly, &tok.ExpiresAt, &tok.LastUsedAt, &tok.RevokedAt, &tok.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", APIToken{}, ErrTokenDuplicate
	}
	if err != nil {
		return "", APIToken{}, fmt.Errorf("session: failed to create API token: %w", err)
	}
	return raw, tok, nil
}

// ListAPITokens returns a user's tokens, revoked ones included (so they can
// see when what was revoked); never the value.
func ListAPITokens(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]APIToken, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, name, prefix, read_only, expires_at, last_used_at, revoked_at, created_at FROM api_tokens WHERE user_id = $1 ORDER BY revoked_at IS NOT NULL, created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.ReadOnly, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken revokes one of the user's tokens (only their own: the id
// goes together with user_id in the WHERE).
func RevokeAPIToken(ctx context.Context, pool *pgxpool.Pool, userID, id int64) (APIToken, error) {
	var t APIToken
	err := pool.QueryRow(ctx,
		`UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
		 RETURNING id, name, prefix, read_only, expires_at, last_used_at, revoked_at, created_at`, id, userID,
	).Scan(&t.ID, &t.Name, &t.Prefix, &t.ReadOnly, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIToken{}, ErrTokenNotFound
	}
	return t, err
}

// revokeAPITokensForUser revokes every active token of a user; called by
// RevokeAllForUser (disabling a user, password change).
func revokeAPITokensForUser(ctx context.Context, pool *pgxpool.Pool, userID int64) error {
	_, err := pool.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return err
}
