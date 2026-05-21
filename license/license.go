// Package license is a thin wrapper around the vendored pkglicense package
// that adds the bits artifact-gateway needs: a sentinel-error CheckActive
// helper and an in-process cache invalidated over NATS.
package license

import (
	"errors"
	"fmt"
	"time"

	pkglicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
)

// Re-export the underlying License type so callers don't import two packages.
type License = pkglicense.License

// Sentinel errors returned by CheckActive. Use errors.Is to test.
var (
	ErrParse            = errors.New("license: parse failed")
	ErrInvalidSignature = errors.New("license: invalid signature")
	ErrExpired          = errors.New("license: expired")
	ErrRevoked          = errors.New("license: revoked")
	ErrMismatch         = errors.New("license: id mismatch with store record")
)

// Verifier parses + verifies a raw .lic blob. The default impl wraps
// pkglicense.Parse; tests can substitute a fake.
type Verifier interface {
	VerifyLicenseBlob(raw string) (*License, error)
}

type defaultVerifier struct{}

// NewVerifier returns the production Verifier.
func NewVerifier() Verifier { return defaultVerifier{} }

// VerifyLicenseBlob parses raw and translates upstream parse/signature
// errors into the sentinel errors exported by this package.
func (defaultVerifier) VerifyLicenseBlob(raw string) (*License, error) {
	l, err := pkglicense.Parse(raw)
	if err != nil {
		return nil, classifyParseError(err)
	}
	return l, nil
}

// classifyParseError maps upstream errors onto our two coarse buckets:
// ErrInvalidSignature for crypto failures, ErrParse for everything else.
func classifyParseError(err error) error {
	switch {
	case errors.Is(err, pkglicense.ErrVerifyFailed):
		return fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	case errors.Is(err, pkglicense.ErrInvalidSignature):
		return fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	default:
		return fmt.Errorf("%w: %v", ErrParse, err)
	}
}

// CheckActive returns nil if the license is usable: not revoked, not expired,
// and (when storeLicenseID is non-empty) its embedded ID matches the store row.
// expectedID and revokedAt come from store.License; l is the parsed payload.
func CheckActive(l *License, revokedAt *time.Time, expectedID string) error {
	if l == nil {
		return ErrParse
	}
	if revokedAt != nil {
		return ErrRevoked
	}
	if l.IsExpired() {
		return ErrExpired
	}
	if expectedID != "" && l.ID != expectedID {
		return fmt.Errorf("%w: parsed=%s stored=%s", ErrMismatch, l.ID, expectedID)
	}
	return nil
}

// VerifyAndCheck is the common path: parse the blob, then run CheckActive
// against the store-supplied revocation/ID fields. Returns the parsed license
// alongside any error (the license may be non-nil even when the error is
// ErrExpired/ErrRevoked, so callers can log details).
func VerifyAndCheck(v Verifier, rawBlob string, revokedAt *time.Time, expectedID string) (*License, error) {
	l, err := v.VerifyLicenseBlob(rawBlob)
	if err != nil {
		return nil, err
	}
	if err := CheckActive(l, revokedAt, expectedID); err != nil {
		return l, err
	}
	return l, nil
}
