// Package auth implements local login: password hashing, generation of the
// initial administrator password and its "forced change on first access"
// cycle. SSO login with Entra ID (Phase 2) lives in a separate package to keep
// the two authentication methods decoupled, both resolving to the same
// authorization model (rbac).
package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength is the minimum length required of any password chosen by
// a user (initial setup or password change).
const MinPasswordLength = 12

// ErrWeakPassword marks a password policy rejection. Handlers return the
// message of an error wrapping it to the user verbatim; any other error is
// internal and is not (it can carry wrapped driver errors).
var ErrWeakPassword = errors.New("auth: weak password")

// ValidatePasswordStrength applies the minimum password policy.
// MaxPasswordBytes is bcrypt's input limit.
const MaxPasswordBytes = 72

func ValidatePasswordStrength(plain string) error {
	if len(plain) < MinPasswordLength {
		return &policyError{fmt.Sprintf("the password must be at least %d characters long", MinPasswordLength)}
	}
	// bcrypt only hashes the first 72 bytes and this x/crypto refuses longer
	// input: said here as a policy, not as a 500.
	if len(plain) > MaxPasswordBytes {
		return &policyError{fmt.Sprintf("the password must be at most %d bytes long", MaxPasswordBytes)}
	}
	return nil
}

// policyError carries a user-facing message and matches ErrWeakPassword.
type policyError struct{ msg string }

func (e *policyError) Error() string        { return e.msg }
func (e *policyError) Is(target error) bool { return target == ErrWeakPassword }

// HashPassword computes the bcrypt hash of a plaintext password.
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: failed to generate bcrypt hash: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword compares a plaintext password against its bcrypt hash.
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
