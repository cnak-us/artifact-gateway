package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/cnak-us/artifact-gateway/store"

	agoidc "github.com/cnak-us/artifact-gateway/oidc"
)

// --- branding (runtime white-label overrides) -------------------------------
//
// Three endpoints:
//   - GET  /api/v1/branding (admin)   — read current persisted overrides
//   - PUT  /api/v1/branding (admin)   — replace persisted overrides
//   - GET  /api/branding    (public)  — bootstrap fetch from the UI loader,
//                                       called before the user signs in so
//                                       catalog visitors get their brand.
//
// JSON shape uses snake_case keys matching the SQL columns. Empty strings
// mean "no override" — the UI falls back to the compiled-in CNAK preset for
// that field.

type brandingDTO struct {
	ProductName        string    `json:"product_name"`
	Vendor             string    `json:"vendor"`
	VendorShort        string    `json:"vendor_short"`
	FooterTagline      string    `json:"footer_tagline"`
	EmbeddedTagline    string    `json:"embedded_tagline"`
	CatalogHeroEyebrow string    `json:"catalog_hero_eyebrow"`
	HTMLTitle          string    `json:"html_title"`
	MetaDescription    string    `json:"meta_description"`
	AccentLightMain    string    `json:"accent_light_main"`
	AccentLightText    string    `json:"accent_light_text"`
	AccentDarkMain     string    `json:"accent_dark_main"`
	AccentDarkText     string    `json:"accent_dark_text"`
	LogoSVG            string    `json:"logo_svg"`
	SupportEmail       string    `json:"support_email"`
	UpdatedAt          time.Time `json:"updated_at"`
	UpdatedBy          string    `json:"updated_by"`
}

func brandingToDTO(b *store.Branding) brandingDTO {
	return brandingDTO{
		ProductName:        b.ProductName,
		Vendor:             b.Vendor,
		VendorShort:        b.VendorShort,
		FooterTagline:      b.FooterTagline,
		EmbeddedTagline:    b.EmbeddedTagline,
		CatalogHeroEyebrow: b.CatalogHeroEyebrow,
		HTMLTitle:          b.HTMLTitle,
		MetaDescription:    b.MetaDescription,
		AccentLightMain:    b.AccentLightMain,
		AccentLightText:    b.AccentLightText,
		AccentDarkMain:     b.AccentDarkMain,
		AccentDarkText:     b.AccentDarkText,
		LogoSVG:            b.LogoSVG,
		SupportEmail:       b.SupportEmail,
		UpdatedAt:          b.UpdatedAt,
		UpdatedBy:          b.UpdatedBy,
	}
}

// maxSupportEmailLen is RFC 5321's local-part + "@" + domain length cap.
// Anything longer is almost certainly a copy-paste accident and not a real
// address; rejecting at the boundary keeps malformed input out of the table.
const maxSupportEmailLen = 254

// validateSupportEmail accepts an empty string (no override — UI falls back
// to the compiled-in preset) or a syntactically valid RFC 5322 address with
// no control characters. It deliberately rejects forms like
// `"Display Name" <a@b>` because the UI only renders bare mailto: targets.
func validateSupportEmail(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > maxSupportEmailLen {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	addr, err := mail.ParseAddress(v)
	if err != nil {
		return false
	}
	// Reject "Display <local@domain>" — addr.Address is the bare email.
	return addr.Address == v
}

// accentRE matches an "R G B" triplet (1-3 digits each, single spaces). The
// per-component 0-255 bound is checked in validateAccent — keeping it out of
// the regex keeps the error message useful.
var accentRE = regexp.MustCompile(`^(\d{1,3})\s(\d{1,3})\s(\d{1,3})$`)

const maxLogoBytes = 64 * 1024

// writeBrandingErr emits the {"error":{"code","message"}} envelope used for
// validation failures — the customization UI keys off `code` to show inline
// hints next to the offending field.
func writeBrandingErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, msg)
}

// validateAccent returns true when the triplet is acceptable (empty is OK —
// caller already trimmed). Each component must be 0..255.
func validateAccent(v string) bool {
	if v == "" {
		return true
	}
	m := accentRE.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		var n int
		if _, err := fmt.Sscanf(m[i], "%d", &n); err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

// decodeBranding parses + validates the request body. On error it writes the
// response itself and returns ok=false so handlers can `return` cleanly.
func decodeBranding(w http.ResponseWriter, r *http.Request) (*store.Branding, bool) {
	var in brandingDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeBrandingErr(w, http.StatusBadRequest, "branding_invalid_body", "invalid JSON body")
		return nil, false
	}
	b := &store.Branding{
		ProductName:        strings.TrimSpace(in.ProductName),
		Vendor:             strings.TrimSpace(in.Vendor),
		VendorShort:        strings.TrimSpace(in.VendorShort),
		FooterTagline:      strings.TrimSpace(in.FooterTagline),
		EmbeddedTagline:    strings.TrimSpace(in.EmbeddedTagline),
		CatalogHeroEyebrow: strings.TrimSpace(in.CatalogHeroEyebrow),
		HTMLTitle:          strings.TrimSpace(in.HTMLTitle),
		MetaDescription:    strings.TrimSpace(in.MetaDescription),
		AccentLightMain:    strings.TrimSpace(in.AccentLightMain),
		AccentLightText:    strings.TrimSpace(in.AccentLightText),
		AccentDarkMain:     strings.TrimSpace(in.AccentDarkMain),
		AccentDarkText:     strings.TrimSpace(in.AccentDarkText),
		LogoSVG:            strings.TrimSpace(in.LogoSVG),
		SupportEmail:       strings.TrimSpace(in.SupportEmail),
	}
	for _, a := range []string{b.AccentLightMain, b.AccentLightText, b.AccentDarkMain, b.AccentDarkText} {
		if !validateAccent(a) {
			writeBrandingErr(w, http.StatusBadRequest, "branding_invalid_accent",
				`accent color must be three 0-255 integers separated by single spaces, e.g. "56 113 220"`)
			return nil, false
		}
	}
	if !validateSupportEmail(b.SupportEmail) {
		writeBrandingErr(w, http.StatusBadRequest, "branding_invalid_support_email",
			"support_email must be a valid email address (or empty)")
		return nil, false
	}
	if b.LogoSVG != "" {
		if len(b.LogoSVG) > maxLogoBytes {
			writeBrandingErr(w, http.StatusBadRequest, "branding_invalid_logo",
				"logo_svg exceeds 64 KiB")
			return nil, false
		}
		if !strings.HasPrefix(b.LogoSVG, "<svg") {
			writeBrandingErr(w, http.StatusBadRequest, "branding_invalid_logo",
				"logo_svg must start with <svg")
			return nil, false
		}
	}
	return b, true
}

func handleGetBranding(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := d.Store.GetBranding(r.Context())
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, brandingToDTO(b))
	}
}

func handlePutBranding(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, ok := decodeBranding(w, r)
		if !ok {
			return
		}
		s := agoidc.SessionFrom(r.Context())
		b.UpdatedBy = actorEmail(s)
		if err := d.Store.SetBranding(r.Context(), b); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Audit summary — skip the (potentially large) logo_svg payload itself;
		// just record whether one was supplied and which fields are non-empty.
		fieldsSet := []string{}
		for _, kv := range []struct {
			name string
			v    string
		}{
			{"product_name", b.ProductName}, {"vendor", b.Vendor}, {"vendor_short", b.VendorShort},
			{"footer_tagline", b.FooterTagline}, {"embedded_tagline", b.EmbeddedTagline},
			{"catalog_hero_eyebrow", b.CatalogHeroEyebrow}, {"html_title", b.HTMLTitle},
			{"meta_description", b.MetaDescription},
			{"accent_light_main", b.AccentLightMain}, {"accent_light_text", b.AccentLightText},
			{"accent_dark_main", b.AccentDarkMain}, {"accent_dark_text", b.AccentDarkText},
		} {
			if kv.v != "" {
				fieldsSet = append(fieldsSet, kv.name)
			}
		}
		// Surface presence of support_email separately so the audit log keeps
		// a binary "was it set" signal without recording the address itself.
		if b.SupportEmail != "" {
			fieldsSet = append(fieldsSet, "support_email")
		}
		summary, _ := json.Marshal(map[string]any{
			"logo_changed": b.LogoSVG != "",
			"fields_set":   fieldsSet,
		})
		d.Auditor.LogResourceMutation(actorEmail(s), "branding.update", "branding", "1", string(summary), clientIP(r))

		writeJSON(w, http.StatusOK, brandingToDTO(b))
	}
}

// handlePublicBranding serves GET /api/branding without auth. Catalog visitors
// hit this on every page load via the UI bootstrap, so it MUST stay public —
// gating it behind RequireAdmin would brick the unauthenticated catalog brand.
// All fields are presentation-only (UI strings, colors, logo SVG); nothing
// sensitive lives in the branding table.
func handlePublicBranding(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := d.Store.GetBranding(r.Context())
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, brandingToDTO(b))
	}
}
