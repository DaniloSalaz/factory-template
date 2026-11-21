package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// randomBytes is the size of every random value: session tokens, states and PKCE
// verifiers. 32 bytes encode to 43 base64url characters.
const (
	randomBytes   = 32
	encodedLength = 43
)

// NewRandom returns 32 bytes from crypto/rand, base64url-encoded without padding.
func NewRandom() (string, error) {
	b := make([]byte, randomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// WellFormed reports whether s has the shape NewRandom produces, so malformed cookies or
// states are refused before any lookup.
func WellFormed(s string) bool {
	if len(s) != encodedLength {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

// HashToken returns the SHA-256 of a session token: the only form stored in the
// database (auth-y-tenancy §5.1.9).
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// CodeChallenge returns the PKCE S256 challenge of verifier (RFC 7636).
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
