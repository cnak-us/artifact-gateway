package license

import (
	"crypto/ed25519"

	pkglicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
)

// PubKeyProvider returns the set of currently-trusted Ed25519 public keys.
// Called on every license verification (only on license-cache miss in
// practice). The vendored pkglicense.PublicKey is always tried in addition
// to whatever the provider returns, so cnak-signed legacy blobs keep working.
type PubKeyProvider func() []ed25519.PublicKey

type storeVerifier struct {
	provider PubKeyProvider
}

// NewStoreVerifier returns a Verifier that tries each provided key (plus the
// vendored fallback) and returns the first successful parse. A nil provider
// is treated as an empty list — behavior collapses to the vendored-key-only
// path, matching NewVerifier.
func NewStoreVerifier(provider PubKeyProvider) Verifier {
	return storeVerifier{provider: provider}
}

func (v storeVerifier) VerifyLicenseBlob(raw string) (*License, error) {
	var keys []ed25519.PublicKey
	if v.provider != nil {
		for _, k := range v.provider() {
			if len(k) != ed25519.PublicKeySize {
				continue
			}
			keys = append(keys, k)
		}
	}
	if len(pkglicense.PublicKey) > 0 {
		keys = append(keys, pkglicense.PublicKey)
	}

	var lastErr error
	for _, k := range keys {
		l, err := pkglicense.ParseWithKey(raw, k)
		if err == nil {
			return l, nil
		}
		lastErr = err
	}
	return nil, classifyParseError(lastErr)
}
