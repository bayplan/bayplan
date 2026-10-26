package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "encryption-key")
	first, err := LoadOrCreateKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewKeyring("V1", map[KeyID]string{"V1": first}); err != nil {
		t.Fatalf("generated key is not usable: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	second, err := LoadOrCreateKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("the key changed between two loads")
	}

	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeyFile(path); err == nil {
		t.Fatal("a corrupt key file must be an error, never silently replaced")
	}
}
