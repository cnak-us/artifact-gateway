package server

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	cnaklicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
)

func TestParseLicenseDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr string // substring; empty means no error
	}{
		{in: "365d", want: 365 * 24 * time.Hour},
		{in: "1d", want: 24 * time.Hour},
		{in: "2y", want: 2 * 365 * 24 * time.Hour},
		{in: "24h", want: 24 * time.Hour},
		{in: "0d", wantErr: "must be positive"},
		{in: "-1d", wantErr: "must be positive"},
		{in: "9999y", wantErr: "exceeds maximum"},
		{in: "999999d", wantErr: "exceeds maximum"},
		{in: "garbage", wantErr: "invalid duration"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseLicenseDuration(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("got nil error, want %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIssuanceRoundTrip is the load-bearing assertion that our server-side
// signing path produces a blob the existing license library can verify. It
// bypasses the HTTP/DB layers and just exercises the crypto step the issue
// handler relies on.
func TestIssuanceRoundTrip(t *testing.T) {
	pub, priv, err := cnaklicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	now := time.Now().UTC()
	original := &cnaklicense.License{
		ID:           "lic_test_123",
		Customer:     "Acme Corp",
		Organization: "Acme Inc",
		Tier:         cnaklicense.TierProfessional,
		MaxTracks:    50000,
		IssuedAt:     now.Format(time.RFC3339),
		ExpiresAt:    now.Add(365 * 24 * time.Hour).Format(time.RFC3339),
	}

	blob, err := cnaklicense.IssueLicense(priv, original)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	parsed, err := cnaklicense.ParseWithKey(blob, pub)
	if err != nil {
		t.Fatalf("parse with issuer pubkey: %v", err)
	}
	if parsed.ID != original.ID || parsed.Customer != original.Customer {
		t.Fatalf("payload mismatch: parsed=%+v original=%+v", parsed, original)
	}

	// Cross-key check: a parse against the wrong public key must fail.
	otherPub, _, err := cnaklicense.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	if _, err := cnaklicense.ParseWithKey(blob, otherPub); err == nil {
		t.Fatalf("parse with wrong pubkey unexpectedly succeeded")
	}
}

func TestFingerprintPubkeyDeterministic(t *testing.T) {
	pub := ed25519.PublicKey{
		0x77, 0x1c, 0x72, 0xe4, 0xf6, 0xea, 0x35, 0x4a,
		0xa0, 0x20, 0x47, 0x28, 0x3c, 0xbd, 0x15, 0x10,
		0xbd, 0x9c, 0x43, 0xaa, 0x29, 0x31, 0xfa, 0x5c,
		0xcd, 0xa2, 0x3f, 0xd0, 0xe1, 0xfe, 0xf0, 0xb3,
	}
	got := fingerprintPubkey(pub)
	if len(got) != 16 {
		t.Fatalf("fingerprint length = %d, want 16", len(got))
	}
	if got != fingerprintPubkey(pub) {
		t.Fatal("fingerprint not deterministic")
	}
}
