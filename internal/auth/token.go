package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"

	"github.com/rm4n0s/errors"
)

// tokenBytes is the number of random bytes in a token.
const tokenBytes = 32

// NewToken returns a random token: 32 bytes from the system's random source,
// in base64url without padding (43 characters). Session and invitation tokens
// are made with it; the database keeps only HashToken of them.
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errors.NewErr(tagTokenGenerationFailed, err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the SHA-256 of token as 64 lowercase hex characters.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokensEqual reports whether a and b are the same string, in time that does
// not depend on where they differ.
func TokensEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// tagTokenGenerationFailed marks a failure of the random source while making a
// token.
const tagTokenGenerationFailed = "TokenGenerationFailed"
