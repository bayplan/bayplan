package auth

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxUsernameLength bounds a username in runes. It is shown in tables,
// breadcrumbs and the audit log, and impersonated as "bayplan:<username>"
// against every managed cluster.
const MaxUsernameLength = 64

// ErrInvalidUsername is returned by ValidateUsername; its message is safe to
// show to the user.
var ErrInvalidUsername = errors.New("username must be 1-64 characters, without control characters or leading/trailing spaces")

// ValidateUsername applies the same rule to local users and to usernames
// derived from an OIDC claim: non-empty, at most MaxUsernameLength runes,
// valid UTF-8, no control characters and no surrounding whitespace. A control
// character (newline, NUL) in a name ends up in the audit CSV, in log lines
// and in the Impersonate-User header sent to the clusters, where client-go
// would reject it on every write; better to refuse it at the door.
func ValidateUsername(name string) error {
	if name == "" || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return ErrInvalidUsername
	}
	if utf8.RuneCountInString(name) > MaxUsernameLength {
		return ErrInvalidUsername
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return ErrInvalidUsername
		}
	}
	return nil
}
