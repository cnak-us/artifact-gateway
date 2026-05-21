// Package pkglicense is the artifact-gateway-local copy of the license
// payload + signing primitives that used to live in
// github.com/cnak-us/cnak/pkg/license. It was vendored so the gateway can
// build in CI without a sibling checkout of the cnak monorepo. Keep the
// wire format byte-for-byte compatible with the upstream package — issued
// .lic blobs must remain verifiable by anything still reading the original.
package pkglicense

import "time"

const (
	TierTrial        = "trial"
	TierProfessional = "professional"
	TierEnterprise   = "enterprise"
)

const TrialDuration = 30 * 24 * time.Hour

// License is the payload inside a .lic file. encoding/json sorts map keys
// alphabetically, so Attributes can be added without breaking signature
// determinism. Pre-attributes licenses verify unchanged because verify reads
// the raw signed payload bytes and never re-serializes.
type License struct {
	ID           string `json:"id"`
	Customer     string `json:"customer"`
	Organization string `json:"organization,omitempty"`
	POCName      string `json:"poc_name,omitempty"`
	POCEmail     string `json:"poc_email,omitempty"`
	Tier         string `json:"tier"`
	// MaxTracks is retained at the top level for backwards compatibility with
	// licenses issued before Attributes existed. New issuance prefers
	// Attributes["max_tracks"].
	MaxTracks  int               `json:"max_tracks,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	IssuedAt   string            `json:"issued_at"`
	ExpiresAt  string            `json:"expires_at,omitempty"`
}

func (l *License) Attr(key string) string {
	if l == nil || l.Attributes == nil {
		return ""
	}
	return l.Attributes[key]
}

// IsExpired reports whether the license is past its ExpiresAt. An empty
// ExpiresAt means perpetual.
func (l *License) IsExpired() bool {
	if l.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, l.ExpiresAt)
	if err != nil {
		return true
	}
	return time.Now().After(t)
}

func (l *License) TierAtLeast(tier string) bool {
	order := map[string]int{
		TierTrial:        0,
		TierProfessional: 1,
		TierEnterprise:   2,
	}
	return order[l.Tier] >= order[tier]
}

type LicenseStatus struct {
	Licensed      bool              `json:"licensed"`
	Tier          string            `json:"tier"`
	Customer      string            `json:"customer,omitempty"`
	Organization  string            `json:"organization,omitempty"`
	POCName       string            `json:"poc_name,omitempty"`
	POCEmail      string            `json:"poc_email,omitempty"`
	MaxTracks     int               `json:"max_tracks,omitempty"`
	Attributes    map[string]string `json:"attributes,omitempty"`
	ExpiresAt     string            `json:"expires_at,omitempty"`
	Trial         bool              `json:"trial"`
	TrialDaysLeft int               `json:"trial_days_left,omitempty"`
	Expired       bool              `json:"expired"`
}
