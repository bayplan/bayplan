package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidCredentials = errors.New("auth: invalid credentials")

type User struct {
	ID                 int64
	Username           string
	MustChangePassword bool
	Disabled           bool
}

// dummyHash is compared against passwords when the user does not exist, so
// that Authenticate takes the same time to respond whether or not the user
// exists, making user enumeration by timing harder.
var dummyHash = mustHashOnce("bayplan-timing-safety-dummy-password-placeholder")

func mustHashOnce(s string) string {
	h, err := HashPassword(s)
	if err != nil {
		panic(err)
	}
	return h
}

// Authenticate validates a local username+password. It always returns the
// same generic error for every failure cause (nonexistent user, disabled, no
// local password yet, wrong password) so as not to leak which users exist.
func Authenticate(ctx context.Context, pool *pgxpool.Pool, username, password string) (*User, error) {
	var (
		u    User
		hash *string
	)
	err := pool.QueryRow(ctx,
		`SELECT id, username, password_hash, must_change_password, disabled
		 FROM users WHERE username = $1 AND source = 'local'`,
		username,
	).Scan(&u.ID, &u.Username, &hash, &u.MustChangePassword, &u.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		VerifyPassword(dummyHash, password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("auth: failed to query user: %w", err)
	}
	if u.Disabled || hash == nil {
		VerifyPassword(dummyHash, password)
		return nil, ErrInvalidCredentials
	}
	if !VerifyPassword(*hash, password) {
		return nil, ErrInvalidCredentials
	}
	return &u, nil
}

// ChangePassword updates the password of an already authenticated user,
// verifying the current password first.
func ChangePassword(ctx context.Context, pool *pgxpool.Pool, userID int64, currentPassword, newPassword string) error {
	var hash *string
	err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&hash)
	if err != nil {
		return fmt.Errorf("auth: failed to query user: %w", err)
	}
	if hash == nil || !VerifyPassword(*hash, currentPassword) {
		return ErrInvalidCredentials
	}
	if err := ValidatePasswordStrength(newPassword); err != nil {
		return err
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, must_change_password = FALSE, updated_at = now() WHERE id = $2`,
		newHash, userID,
	); err != nil {
		return fmt.Errorf("auth: failed to update password: %w", err)
	}
	return nil
}
