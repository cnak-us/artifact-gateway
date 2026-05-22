package license_test

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	pkglicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
	"github.com/cnak-us/artifact-gateway/license"
)

func mkSignedLicense(t *testing.T, priv ed25519.PrivateKey, id string) string {
	t.Helper()
	blob, err := pkglicense.IssueLicense(priv, &pkglicense.License{
		ID:       id,
		Customer: "Acme",
		Tier:     pkglicense.TierEnterprise,
		IssuedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("issue license: %v", err)
	}
	return blob
}

func TestStoreVerifier_ProviderListedKeyVerifies(t *testing.T) {
	pubA, privA, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	blob := mkSignedLicense(t, privA, "lic-provider-ok")

	v := license.NewStoreVerifier(func() []ed25519.PublicKey {
		return []ed25519.PublicKey{pubA}
	})

	got, err := v.VerifyLicenseBlob(blob)
	if err != nil {
		t.Fatalf("VerifyLicenseBlob: %v", err)
	}
	if got == nil || got.ID != "lic-provider-ok" {
		t.Fatalf("parsed license = %+v, want ID=lic-provider-ok", got)
	}
}

func TestStoreVerifier_UnknownKeyRejected(t *testing.T) {
	_, privA, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	pubB, _, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key B: %v", err)
	}
	blob := mkSignedLicense(t, privA, "lic-unknown")

	v := license.NewStoreVerifier(func() []ed25519.PublicKey {
		return []ed25519.PublicKey{pubB}
	})

	_, err = v.VerifyLicenseBlob(blob)
	if err == nil {
		t.Fatal("VerifyLicenseBlob: want error, got nil")
	}
	if !errors.Is(err, license.ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestStoreVerifier_NilProviderFallsBackToVendored(t *testing.T) {
	_, privA, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	blob := mkSignedLicense(t, privA, "lic-nil-provider")

	v := license.NewStoreVerifier(nil)

	_, err = v.VerifyLicenseBlob(blob)
	if err == nil {
		t.Fatal("VerifyLicenseBlob with nil provider: want error, got nil")
	}
	if !errors.Is(err, license.ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestStoreVerifier_EmptyProviderResultFallsBackToVendored(t *testing.T) {
	_, privA, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	blob := mkSignedLicense(t, privA, "lic-empty-provider")

	v := license.NewStoreVerifier(func() []ed25519.PublicKey {
		return []ed25519.PublicKey{}
	})

	_, err = v.VerifyLicenseBlob(blob)
	if err == nil {
		t.Fatal("VerifyLicenseBlob with empty provider: want error, got nil")
	}
	if !errors.Is(err, license.ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestStoreVerifier_JunkKeysInProviderTolerated(t *testing.T) {
	pubA, privA, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	blob := mkSignedLicense(t, privA, "lic-junk-keys")

	v := license.NewStoreVerifier(func() []ed25519.PublicKey {
		return []ed25519.PublicKey{nil, ed25519.PublicKey([]byte("not-a-key")), pubA}
	})

	got, err := v.VerifyLicenseBlob(blob)
	if err != nil {
		t.Fatalf("VerifyLicenseBlob: %v", err)
	}
	if got == nil || got.ID != "lic-junk-keys" {
		t.Fatalf("parsed license = %+v, want ID=lic-junk-keys", got)
	}
}

func TestStoreVerifier_MultipleKeysLastMatches(t *testing.T) {
	pubA, privA, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	pubB, _, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key B: %v", err)
	}
	pubC, _, err := pkglicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key C: %v", err)
	}
	blob := mkSignedLicense(t, privA, "lic-last-match")

	v := license.NewStoreVerifier(func() []ed25519.PublicKey {
		return []ed25519.PublicKey{pubB, pubC, pubA}
	})

	got, err := v.VerifyLicenseBlob(blob)
	if err != nil {
		t.Fatalf("VerifyLicenseBlob: %v", err)
	}
	if got == nil || got.ID != "lic-last-match" {
		t.Fatalf("parsed license = %+v, want ID=lic-last-match", got)
	}
}
