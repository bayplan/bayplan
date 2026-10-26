package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func randomKeyBase64(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("could not generate random key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	kr, err := NewKeyring("v1", map[string]string{"v1": randomKeyBase64(t)})
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	plaintext := []byte("service-account-secret-token")
	keyID, ciphertext, err := kr.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if keyID != "v1" {
		t.Fatalf("keyID = %q, want %q", keyID, "v1")
	}

	got, err := kr.Decrypt(keyID, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("Decrypt = %q, want %q", got, plaintext)
	}
}

// TestKeyRotation checks that after rotating the active key (v1 -> v2), data
// encrypted with v1 can still be decrypted while v1 remains in the keyring,
// and that new data is already encrypted with v2.
func TestKeyRotation(t *testing.T) {
	keyV1 := randomKeyBase64(t)
	keyV2 := randomKeyBase64(t)

	krV1, err := NewKeyring("v1", map[string]string{"v1": keyV1})
	if err != nil {
		t.Fatalf("NewKeyring v1: %v", err)
	}
	_, oldCiphertext, err := krV1.Encrypt([]byte("old-data"))
	if err != nil {
		t.Fatalf("Encrypt with v1: %v", err)
	}

	krRotated, err := NewKeyring("v2", map[string]string{"v1": keyV1, "v2": keyV2})
	if err != nil {
		t.Fatalf("rotated NewKeyring: %v", err)
	}

	newKeyID, newCiphertext, err := krRotated.Encrypt([]byte("new-data"))
	if err != nil {
		t.Fatalf("Encrypt with v2: %v", err)
	}
	if newKeyID != "v2" {
		t.Fatalf("after rotating, keyID = %q, want v2", newKeyID)
	}

	gotOld, err := krRotated.Decrypt("v1", oldCiphertext)
	if err != nil {
		t.Fatalf("Decrypt old data after rotation: %v", err)
	}
	if string(gotOld) != "old-data" {
		t.Fatalf("decrypted old data = %q", gotOld)
	}

	gotNew, err := krRotated.Decrypt(newKeyID, newCiphertext)
	if err != nil {
		t.Fatalf("Decrypt new data: %v", err)
	}
	if string(gotNew) != "new-data" {
		t.Fatalf("decrypted new data = %q", gotNew)
	}
}

func TestDecryptWithUnknownKeyIDFails(t *testing.T) {
	kr, err := NewKeyring("v1", map[string]string{"v1": randomKeyBase64(t)})
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	_, ciphertext, err := kr.Encrypt([]byte("x"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := kr.Decrypt("v99-does-not-exist", ciphertext); err == nil {
		t.Fatal("expected an error decrypting with an unknown key_id")
	}
}

func TestNewKeyringRejectsWrongKeyLength(t *testing.T) {
	shortKey := base64.StdEncoding.EncodeToString([]byte("too-short"))
	if _, err := NewKeyring("v1", map[string]string{"v1": shortKey}); err == nil {
		t.Fatal("expected an error for an invalid key length")
	}
}

func TestNewKeyringRequiresActiveKeyPresent(t *testing.T) {
	if _, err := NewKeyring("v1", map[string]string{}); err == nil {
		t.Fatal("expected an error: active key_id missing from the key set")
	}
}
