package config

import (
	"os"
	"testing"
	"time"
)

func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

// unsetEnv removes the variables for the test (t.Setenv first so that they
// are restored afterwards): unset and empty mean different things for
// TLS_LISTEN_ADDR.
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLoadWithoutDatabaseURLIsAllInOne(t *testing.T) {
	unsetEnv(t, "DATABASE_URL", "TLS_LISTEN_ADDR", "DATA_DIR")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Embedded || cfg.DataDir != "/data" || cfg.TLSListenAddr != ":8443" {
		t.Fatalf("all-in-one defaults: embedded=%v dataDir=%q tls=%q", cfg.Embedded, cfg.DataDir, cfg.TLSListenAddr)
	}

	// Explicitly empty disables HTTPS even in all-in-one (TLS done by a proxy).
	t.Setenv("TLS_LISTEN_ADDR", "")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSListenAddr != "" {
		t.Fatalf("TLS_LISTEN_ADDR=\"\" must disable HTTPS, got %q", cfg.TLSListenAddr)
	}
}

func TestLoadExternalDatabaseKeepsPlainHTTP(t *testing.T) {
	unsetEnv(t, "TLS_LISTEN_ADDR")
	t.Setenv("DATABASE_URL", "postgres://localhost/bayplan")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedded || cfg.TLSListenAddr != "" {
		t.Fatalf("with DATABASE_URL nothing changes: embedded=%v tls=%q", cfg.Embedded, cfg.TLSListenAddr)
	}
	t.Setenv("TLS_CERT_FILE", "/x.crt")
	if _, err := Load(); err == nil {
		t.Fatal("a certificate without TLS_LISTEN_ADDR must be rejected, not silently ignored")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/bayplan")
	t.Setenv("ENCRYPTION_KEY_ACTIVE_ID", "v1")
	t.Setenv("ENCRYPTION_KEY_V1", "test-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.SessionIdleTimeout != 30*time.Minute {
		t.Errorf("SessionIdleTimeout = %v, want 30m", cfg.SessionIdleTimeout)
	}
	if cfg.SessionAbsoluteTimeout != 12*time.Hour {
		t.Errorf("SessionAbsoluteTimeout = %v, want 12h", cfg.SessionAbsoluteTimeout)
	}
	if !cfg.CookieSecure {
		t.Error("CookieSecure must be true by default")
	}
	if cfg.EncryptionKeys["V1"] != "test-key" {
		t.Errorf("EncryptionKeys[V1] = %q", cfg.EncryptionKeys["V1"])
	}
}

// TestLoadEncryptionKeyActiveIDCaseInsensitive covers a real regression: when
// wiring crypto.NewKeyring in main.go, ENCRYPTION_KEY_ACTIVE_ID=v1 (following
// the lowercase convention of .env.example) did not match the "V1" key derived
// from the ENCRYPTION_KEY_V1 variable name, and startup failed with "the
// active key is not present in the key set".
func TestLoadEncryptionKeyActiveIDCaseInsensitive(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/bayplan")
	t.Setenv("ENCRYPTION_KEY_ACTIVE_ID", "v1")
	t.Setenv("ENCRYPTION_KEY_V1", "test-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.EncryptionKeys[cfg.EncryptionKeyActiveID]; !ok {
		t.Fatalf("EncryptionKeyActiveID %q does not match any key in %v", cfg.EncryptionKeyActiveID, cfg.EncryptionKeys)
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/bayplan")
	t.Setenv("SESSION_IDLE_TIMEOUT", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for an invalid SESSION_IDLE_TIMEOUT")
	}
}

func TestEntraDisabledByDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/bayplan")
	clearEnv(t, "AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET", "AZURE_REDIRECT_URI")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EntraEnabled() {
		t.Error("EntraEnabled() must be false without any AZURE_* variable")
	}
}

func TestEntraEnabledWithAllFour(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/bayplan")
	t.Setenv("AZURE_TENANT_ID", "tenant")
	t.Setenv("AZURE_CLIENT_ID", "client")
	t.Setenv("AZURE_CLIENT_SECRET", "secret")
	t.Setenv("AZURE_REDIRECT_URI", "https://bayplan.example/api/auth/entra/callback")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.EntraEnabled() {
		t.Error("EntraEnabled() must be true with all 4 AZURE_* variables present")
	}
}

func TestLoadRejectsPartialEntraConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/bayplan")
	t.Setenv("AZURE_TENANT_ID", "tenant")
	t.Setenv("AZURE_CLIENT_ID", "client")
	clearEnv(t, "AZURE_CLIENT_SECRET", "AZURE_REDIRECT_URI")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error with a half-done Entra configuration (2 of 4 variables)")
	}
}
