// Package auth provides authentication primitives for artifact-gateway:
// admin password hashing, customer token generation/verification, OCI bearer
// JWT minting, and AES-GCM envelope encryption for at-rest secrets.
package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const passwordBcryptCost = 12

// ErrInvalidPassword is returned by VerifyPassword when the supplied plaintext
// does not match the stored hash.
var ErrInvalidPassword = errors.New("auth: invalid password")

// HashPassword returns a bcrypt hash of plain at cost 12.
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), passwordBcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword returns nil if plain matches hashed, ErrInvalidPassword on
// mismatch, or the underlying bcrypt error for malformed hashes.
func VerifyPassword(hashed, plain string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(plain))
	if err == nil {
		return nil
	}
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return ErrInvalidPassword
	}
	return err
}
