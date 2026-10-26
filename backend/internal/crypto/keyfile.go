package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateKeyFile returns the base64 AES-256 key stored at path, creating
// it (0600, random) the first time. It exists for the all-in-one mode (one
// container, one volume), where nobody hands the server a key through the
// environment: the key lives in the data volume, next to the data.
// That is weaker than keeping it apart from the database backup, which is why
// the environment keys still win when they are set.
func LoadOrCreateKeyFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		key := strings.TrimSpace(string(raw))
		if dec, err := base64.StdEncoding.DecodeString(key); err != nil || len(dec) != 32 {
			return "", fmt.Errorf("crypto: %s does not hold a base64 32-byte key", path)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("crypto: failed to read %s: %w", path, err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("crypto: failed to generate key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("crypto: %w", err)
	}
	// O_EXCL: two processes starting at once must not each write their own key.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreateKeyFile(path)
		}
		return "", fmt.Errorf("crypto: failed to create %s: %w", path, err)
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return "", fmt.Errorf("crypto: failed to write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("crypto: failed to write %s: %w", path, err)
	}
	return key, nil
}
