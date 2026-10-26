package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidOrExpiredToken = errors.New("auth: setup token is invalid, expired or already used")

// CompleteSetup consumes a one-time setup token (see Bootstrap) and sets the
// password chosen by the user. It fails if the token does not exist, was
// already used or has expired. It uses SELECT ... FOR UPDATE so that two
// concurrent requests with the same token cannot use it twice.
func CompleteSetup(ctx context.Context, pool *pgxpool.Pool, token, newPassword string) (userID int64, err error) {
	if err := ValidatePasswordStrength(newPassword); err != nil {
		return 0, err
	}

	tokenHash := HashBootstrapToken(token)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("auth: setup: failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var expiresAt time.Time
	var usedAt *time.Time
	err = tx.QueryRow(ctx,
		`SELECT user_id, expires_at, used_at FROM bootstrap_tokens WHERE token_hash = $1 FOR UPDATE`,
		tokenHash,
	).Scan(&userID, &expiresAt, &usedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrInvalidOrExpiredToken
	}
	if err != nil {
		return 0, fmt.Errorf("auth: setup: failed to query token: %w", err)
	}
	if usedAt != nil || time.Now().After(expiresAt) {
		return 0, ErrInvalidOrExpiredToken
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET password_hash = $1, must_change_password = FALSE, updated_at = now() WHERE id = $2`,
		hash, userID,
	); err != nil {
		return 0, fmt.Errorf("auth: setup: failed to update password: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE bootstrap_tokens SET used_at = now() WHERE token_hash = $1`,
		tokenHash,
	); err != nil {
		return 0, fmt.Errorf("auth: setup: failed to mark token as used: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("auth: setup: failed to commit transaction: %w", err)
	}
	return userID, nil
}

// SetupPending reports whether any setup token exists that is neither used nor
// expired. The frontend uses it to decide whether to show the initial setup
// screen instead of the login screen.
func SetupPending(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM bootstrap_tokens WHERE used_at IS NULL AND expires_at > now())`,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("auth: failed to check pending setup: %w", err)
	}
	return exists, nil
}
