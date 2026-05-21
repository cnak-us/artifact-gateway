package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	kekLength   = 32
	gcmNonceLen = 12
)

// Crypto wraps the KEK used for AES-GCM envelope encryption of at-rest
// secrets (ghcr PATs, OIDC client secrets).
type Crypto struct {
	aead cipher.AEAD
}

// NewCrypto initialises a Crypto from a base64-encoded 32-byte KEK.
func NewCrypto(kekBase64 string) (*Crypto, error) {
	if kekBase64 == "" {
		return nil, errors.New("auth: KEK is empty")
	}
	key, err := base64.StdEncoding.DecodeString(kekBase64)
	if err != nil {
		return nil, fmt.Errorf("auth: KEK is not valid base64: %w", err)
	}
	if len(key) != kekLength {
		return nil, fmt.Errorf("auth: KEK must be exactly %d bytes, got %d", kekLength, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Crypto{aead: aead}, nil
}

// Seal encrypts plaintext under the KEK. Output is `nonce || ciphertext+tag`,
// suitable for direct storage in a BYTEA column.
func (c *Crypto) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// AEAD.Seal appends to nonce, so the result is nonce||ciphertext||tag.
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal. Returns an error if the blob is too short or the GCM
// tag fails authentication (i.e. the ciphertext was tampered with).
func (c *Crypto) Open(blob []byte) ([]byte, error) {
	if len(blob) < gcmNonceLen+c.aead.Overhead() {
		return nil, errors.New("auth: sealed blob too short")
	}
	nonce, ct := blob[:gcmNonceLen], blob[gcmNonceLen:]
	return c.aead.Open(nil, nonce, ct, nil)
}

// Fingerprint returns the first 8 hex chars of sha256(plaintext). Used to
// render a stable, non-reversible preview of a stored PAT in the admin UI.
func (c *Crypto) Fingerprint(plaintext []byte) string {
	sum := sha256.Sum256(plaintext)
	return hex.EncodeToString(sum[:])[:8]
}
