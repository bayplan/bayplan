package oidc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// newState generates a random value for the OAuth2 "state" parameter (CSRF
// protection of the authorization flow).
func newState() (string, error) {
	return randomURLSafe(32)
}

// pkcePair is the code_verifier (secret, sent in the code exchange) and its
// S256 code_challenge (public, goes in the authorization URL).
type pkcePair struct {
	Verifier  string
	Challenge string
}

// newPKCE generates a verifier/challenge pair per RFC 7636 (S256).
func newPKCE() (pkcePair, error) {
	verifier, err := randomURLSafe(32)
	if err != nil {
		return pkcePair{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return pkcePair{Verifier: verifier, Challenge: challenge}, nil
}

func randomURLSafe(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("oidc: failed generating randomness: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
