package pkglicense

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// GenerateKeyPair creates a new Ed25519 keypair for license signing.
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// IssueLicense signs a license payload with the given private key and
// returns the .lic file content: base64url(json).base64url(sig).
func IssueLicense(privKey ed25519.PrivateKey, lic *License) (string, error) {
	payload, err := json.Marshal(lic)
	if err != nil {
		return "", fmt.Errorf("marshal license: %w", err)
	}

	sig := ed25519.Sign(privKey, payload)

	encoded := base64.RawURLEncoding.EncodeToString(payload) +
		"." +
		base64.RawURLEncoding.EncodeToString(sig)

	return encoded, nil
}

// FormatPublicKeyGo returns Go source code for embedding a public key in
// pubkey.go (used by tooling that bakes a verify key into a binary).
func FormatPublicKeyGo(pub ed25519.PublicKey) string {
	s := "var PublicKey ed25519.PublicKey = []byte{\n"
	for i, b := range pub {
		if i%8 == 0 {
			s += "\t"
		}
		s += fmt.Sprintf("0x%02x, ", b)
		if i%8 == 7 {
			s += "\n"
		}
	}
	s += "\n}"
	return s
}
