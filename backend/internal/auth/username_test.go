package auth

import (
	"strings"
	"testing"
)

func TestValidateUsername(t *testing.T) {
	ok := []string{"admin", "jane.doe@example.com", "José", "a", strings.Repeat("x", MaxUsernameLength), "two words"}
	for _, n := range ok {
		if err := ValidateUsername(n); err != nil {
			t.Errorf("ValidateUsername(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "   ", " lead", "trail ", "new\nline", "nul\x00", "tab\tname", "\u0085x",
		strings.Repeat("x", MaxUsernameLength+1), "bad\xffutf8"}
	for _, n := range bad {
		if err := ValidateUsername(n); err == nil {
			t.Errorf("ValidateUsername(%q) = nil, want error", n)
		}
	}
}
