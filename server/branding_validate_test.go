package server

import "testing"

// TestValidateSupportEmail covers the validation rules used by the branding
// PUT handler. The function is intentionally strict — it rejects display-name
// forms and any control characters — because the value flows directly into a
// `<a href="mailto:...">` in the catalog UI.
func TestValidateSupportEmail(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		name string
	}{
		{"", true, "empty allowed (UI falls back to preset)"},
		{"support@cnak.us", true, "plain RFC 5322 address"},
		{"a@b.io", true, "minimal valid"},
		{"javascript:alert(1)", false, "no URL schemes"},
		{`"Display" <a@b>`, false, "display-name form rejected"},
		{"abc", false, "missing @"},
		{"a@", false, "missing domain"},
		{"@b.io", false, "missing local"},
		{"a@b\x00.io", false, "control char rejected"},
		{"a@b\n.io", false, "newline rejected"},
		// Pathological length: 254 + many chars over.
		{string(make([]byte, 300)), false, "over max length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateSupportEmail(tc.in); got != tc.want {
				t.Fatalf("validateSupportEmail(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
