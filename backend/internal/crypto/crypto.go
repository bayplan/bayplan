// Package crypto encrypts/decrypts the credentials of each cluster's technical
// ServiceAccounts with AES-256-GCM. Every encrypted value is tagged with the
// key_id of the key protecting it, which allows rotating the master key
// without downtime: old keys can still decrypt while data is progressively
// re-encrypted with the new one.
//
// The key and its backups must be kept SEPARATE from the Postgres backups:
// without the key, a database restore cannot decrypt the ServiceAccount
// tokens; if key and dump travel together, a leaked backup compromises every
// managed cluster.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// KeyID is the version identifier of an encryption key (e.g. "v1").
type KeyID = string

// Keyring resolves encryption keys by version. The active key is used to
// encrypt new data; every key (active and historical) is used to decrypt
// according to the key_id stored alongside the data.
type Keyring struct {
	active KeyID
	keys   map[KeyID][]byte // decoded AES-256 key (32 bytes)
}

// NewKeyring builds a Keyring from base64 keys (32 bytes after decoding)
// indexed by key_id, plus the active key_id used for encrypting.
func NewKeyring(activeID KeyID, keysBase64 map[KeyID]string) (*Keyring, error) {
	if activeID == "" {
		return nil, fmt.Errorf("crypto: missing active key_id")
	}
	keys := make(map[KeyID][]byte, len(keysBase64))
	for id, b64 := range keysBase64 {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("crypto: key %q is not valid base64: %w", id, err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("crypto: key %q must be 32 bytes (AES-256), got %d", id, len(raw))
		}
		keys[id] = raw
	}
	if _, ok := keys[activeID]; !ok {
		return nil, fmt.Errorf("crypto: the active key %q is not present in the key set", activeID)
	}
	return &Keyring{active: activeID, keys: keys}, nil
}

// ActiveID returns the key version new data is encrypted with.
func (k *Keyring) ActiveID() KeyID { return k.active }

// Encrypt encrypts plaintext with the active key. It returns the key_id used
// and the ciphertext (nonce || GCM seal).
func (k *Keyring) Encrypt(plaintext []byte) (keyID KeyID, ciphertext []byte, err error) {
	gcm, err := k.gcmFor(k.active)
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, fmt.Errorf("crypto: failed to generate nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return k.active, sealed, nil
}

// Decrypt decrypts ciphertext using the key identified by keyID (active or
// historical). It fails if keyID is not registered in the Keyring.
func (k *Keyring) Decrypt(keyID KeyID, ciphertext []byte) ([]byte, error) {
	gcm, err := k.gcmFor(keyID)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("crypto: ciphertext too short")
	}
	nonce, sealed := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to decrypt (key %q): %w", keyID, err)
	}
	return plaintext, nil
}

func (k *Keyring) gcmFor(keyID KeyID) (cipher.AEAD, error) {
	raw, ok := k.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("crypto: key_id %q not found in the keyring (key rotated without keeping the historical one?)", keyID)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("crypto: invalid AES key: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to initialize GCM: %w", err)
	}
	return gcm, nil
}
