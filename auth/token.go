package auth

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	tokenIDLength     = 20
	secretRandomBytes = 32
	secretBcryptCost  = 12
)

// ErrInvalidSecret is returned by VerifySecret when the supplied secret does
// not match the stored hash.
var ErrInvalidSecret = errors.New("auth: invalid token secret")

// b32 is uppercase, unpadded RFC 4648 base32 — friendly for HTTP Basic and
// shell copy/paste.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GeneratedToken is the in-memory result of GenerateCustomerToken. The full
// credential is shown to the operator exactly once at creation time; only the
// bcrypt hash of Secret is persisted.
type GeneratedToken struct {
	TokenID        string
	Secret         string
	FullCredential string
}

// GenerateCustomerToken creates a new (token_id, secret) pair. TokenID is a
// 20-char uppercase base32 string derived from random bytes; Secret is 32
// random bytes encoded as unpadded base32. FullCredential is `tokenID:secret`,
// directly usable as the password half of HTTP Basic.
func GenerateCustomerToken() (GeneratedToken, error) {
	// 20 base32 chars encode 100 bits → 13 random bytes (104 bits) is enough.
	idBytes := make([]byte, 13)
	if _, err := rand.Read(idBytes); err != nil {
		return GeneratedToken{}, err
	}
	tokenID := b32.EncodeToString(idBytes)[:tokenIDLength]

	secretBytes := make([]byte, secretRandomBytes)
	if _, err := rand.Read(secretBytes); err != nil {
		return GeneratedToken{}, err
	}
	secret := b32.EncodeToString(secretBytes)

	return GeneratedToken{
		TokenID:        tokenID,
		Secret:         secret,
		FullCredential: tokenID + ":" + secret,
	}, nil
}

// HashSecret bcrypts the raw secret at cost 12.
func HashSecret(secret string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(secret), secretBcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifySecret returns nil on match, ErrInvalidSecret on mismatch.
func VerifySecret(hashed, secret string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(secret))
	if err == nil {
		return nil
	}
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return ErrInvalidSecret
	}
	return err
}

// ParseBasic decodes an `Authorization: Basic <base64(id:secret)>` header.
// Returns ok=false on any malformed input — caller should treat as 401.
func ParseBasic(authHeader string) (tokenID, secret string, ok bool) {
	const prefix = "Basic "
	if len(authHeader) <= len(prefix) {
		return "", "", false
	}
	// RFC 7617: scheme name is case-insensitive.
	if !strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return "", "", false
	}
	encoded := strings.TrimSpace(authHeader[len(prefix):])
	if encoded == "" {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}
	idx := strings.IndexByte(string(decoded), ':')
	if idx <= 0 || idx == len(decoded)-1 {
		return "", "", false
	}
	return string(decoded[:idx]), string(decoded[idx+1:]), true
}
