package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const bootstrapTokenTTL = 30 * time.Minute

// Bootstrap creates the initial admin user if the users table is empty.
// Instead of generating and printing a password in the log (the Jenkins
// pattern, which stays permanently in journald), it generates a one-time
// setup TOKEN: the admin uses it exactly once in /setup to set their own
// password. The token never works as a login password.
//
// If the admin exists but never got to set a password (password_hash NULL) and
// no valid token remains, a NEW one is issued on every startup: before, an
// expired unused token left the installation with no way in other than
// touching the DB by hand. Regenerating it is safe because that user cannot
// authenticate by any other means.
//
// With bootstrapPassword (BOOTSTRAP_PASSWORD, for unattended installs: Helm
// values, Ansible) the admin is created with that password instead of a token,
// and must_change_password forces a new one on first login, so the value left
// in a Secret or an env file stops working once someone has logged in.
func Bootstrap(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, bootstrapPassword string) error {
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return fmt.Errorf("auth: bootstrap: failed to check existing users: %w", err)
	}

	var userID int64
	if count > 0 {
		// Is there a local admin without a password and without a valid token?
		// If not, nothing to do.
		err := pool.QueryRow(ctx,
			`SELECT u.id FROM users u
			 WHERE u.source = 'local' AND u.password_hash IS NULL AND u.disabled = FALSE
			   AND EXISTS (SELECT 1 FROM role_assignments ra WHERE ra.user_id = u.id AND ra.role = 'admin' AND ra.scope_type = 'global')
			   AND NOT EXISTS (SELECT 1 FROM bootstrap_tokens bt WHERE bt.user_id = u.id AND bt.used_at IS NULL AND bt.expires_at > now())
			 ORDER BY u.id LIMIT 1`,
		).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("auth: bootstrap: failed to check for admin without password: %w", err)
		}
		logger.Warn("auth: the initial admin still has no password and its token expired; issuing a new one")
	} else {
		var hash *string
		if bootstrapPassword != "" {
			if err := ValidatePasswordStrength(bootstrapPassword); err != nil {
				return fmt.Errorf("auth: BOOTSTRAP_PASSWORD: %w", err)
			}
			h, err := HashPassword(bootstrapPassword)
			if err != nil {
				return err
			}
			hash = &h
		}
		err := pool.QueryRow(ctx,
			`INSERT INTO users (username, source, password_hash, must_change_password) VALUES ('admin', 'local', $1, TRUE) RETURNING id`,
			hash,
		).Scan(&userID)
		if err != nil {
			return fmt.Errorf("auth: bootstrap: failed to create admin user: %w", err)
		}

		if _, err := pool.Exec(ctx,
			`INSERT INTO role_assignments (user_id, role, scope_type) VALUES ($1, 'admin', 'global')`,
			userID,
		); err != nil {
			return fmt.Errorf("auth: bootstrap: failed to assign admin role: %w", err)
		}
		if hash != nil {
			logger.Warn("First startup: admin created with BOOTSTRAP_PASSWORD; it has to be changed on first login")
			return nil
		}
	}

	token, tokenHash, err := newBootstrapToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(bootstrapTokenTTL)
	if _, err := pool.Exec(ctx,
		`INSERT INTO bootstrap_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt,
	); err != nil {
		return fmt.Errorf("auth: bootstrap: failed to store setup token: %w", err)
	}

	logger.Warn("═══════════════════════════════════════════════════════════════")
	logger.Warn("First startup: set the admin password at /setup")
	logger.Warn("One-time setup token (expires in 30 minutes):")
	logger.Warn(token)
	logger.Warn("═══════════════════════════════════════════════════════════════")

	return nil
}

func newBootstrapToken() (token string, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("auth: failed to generate setup token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashBootstrapToken(token), nil
}

// HashBootstrapToken computes the hash of a setup token, to look it up in
// bootstrap_tokens without ever storing the plaintext value.
func HashBootstrapToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
