package pkglicense

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

var (
	ErrInvalidFormat    = errors.New("license: invalid format, expected payload.signature")
	ErrInvalidPayload   = errors.New("license: failed to decode payload")
	ErrInvalidSignature = errors.New("license: failed to decode signature")
	ErrVerifyFailed     = errors.New("license: signature verification failed")
	ErrMalformedPayload = errors.New("license: malformed payload JSON")
)

// Parse decodes and verifies a .lic file content string against the embedded
// PublicKey (see pubkey.go).
// Format: base64url(json_payload).base64url(ed25519_signature)
func Parse(raw string) (*License, error) {
	return ParseWithKey(raw, PublicKey)
}

// ParseWithKey decodes and verifies a license with a specific public key.
func ParseWithKey(raw string, pubKey ed25519.PublicKey) (*License, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return nil, ErrInvalidFormat
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidPayload
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidSignature
	}

	if !ed25519.Verify(pubKey, payloadBytes, sigBytes) {
		return nil, ErrVerifyFailed
	}

	var lic License
	if err := json.Unmarshal(payloadBytes, &lic); err != nil {
		return nil, ErrMalformedPayload
	}

	return &lic, nil
}
