package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/store"
	cnaklicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	agoidc "github.com/cnak-us/artifact-gateway/oidc"
)

// --- root keys --------------------------------------------------------------

type rootKeyOut struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Fingerprint   string    `json:"fingerprint"`
	HasPrivateKey bool      `json:"has_private_key"`
	Active        bool      `json:"active"`
	ImportedFrom  string    `json:"imported_from,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type rootKeyIn struct {
	Name          string `json:"name"`
	Mode          string `json:"mode"` // "generate" | "upload"
	PrivateKeyHex string `json:"private_key_hex,omitempty"`
}

// rootKeyCreated wraps rootKeyOut with the freshly-generated private key hex —
// returned only on `mode: "generate"`, only on the create response, never
// stored in cleartext. The admin UI shows this once with a stern warning.
type rootKeyCreated struct {
	rootKeyOut
	PrivateKeyHex string `json:"private_key_hex,omitempty"`
}

func rootKeyToOut(k *store.RootKey) rootKeyOut {
	return rootKeyOut{
		ID: k.ID, Name: k.Name, Fingerprint: k.Fingerprint,
		HasPrivateKey: k.HasPrivateKey(), Active: k.Active,
		ImportedFrom: k.ImportedFrom,
		CreatedAt:    k.CreatedAt, UpdatedAt: k.UpdatedAt,
	}
}

func fingerprintPubkey(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

func listRootKeys(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Store.ListRootKeys(r.Context())
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]rootKeyOut, 0, len(rows))
		for i := range rows {
			out = append(out, rootKeyToOut(&rows[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func createRootKey(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in rootKeyIn
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			writeJSONErr(w, http.StatusBadRequest, "name required")
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		mode := strings.ToLower(strings.TrimSpace(in.Mode))
		if mode != "generate" && mode != "upload" {
			writeJSONErr(w, http.StatusBadRequest, `mode must be "generate" or "upload"`)
			return
		}

		var (
			pub          ed25519.PublicKey
			priv         ed25519.PrivateKey
			generatedHex string
		)
		if mode == "generate" {
			var err error
			pub, priv, err = cnaklicense.GenerateKeyPair()
			if err != nil {
				writeJSONErr(w, http.StatusInternalServerError, "generate keypair: "+err.Error())
				return
			}
			generatedHex = hex.EncodeToString(priv)
		} else {
			cleaned := strings.Map(func(r rune) rune {
				if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
					return -1
				}
				return r
			}, in.PrivateKeyHex)
			raw, err := hex.DecodeString(cleaned)
			if err != nil {
				writeJSONErr(w, http.StatusBadRequest, "private_key_hex is not valid hex")
				return
			}
			if len(raw) != ed25519.PrivateKeySize {
				writeJSONErr(w, http.StatusBadRequest,
					fmt.Sprintf("private_key_hex must decode to %d bytes (got %d)", ed25519.PrivateKeySize, len(raw)))
				return
			}
			priv = ed25519.PrivateKey(raw)
			derived, ok := priv.Public().(ed25519.PublicKey)
			if !ok {
				writeJSONErr(w, http.StatusBadRequest, "could not derive public key from private key")
				return
			}
			pub = derived
		}

		sealed, err := d.Crypto.Seal(priv)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "seal private key: "+err.Error())
			return
		}
		fp := fingerprintPubkey(pub)

		row := &store.RootKey{
			ID:            uuid.New(),
			Name:          in.Name,
			PublicKey:     []byte(pub),
			PrivateKeyEnc: sealed,
			Fingerprint:   fp,
			Active:        false,
			ImportedFrom:  mode,
		}
		if err := d.Store.InsertRootKey(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "create", "root-key", row.ID.String(), row.Name, clientIP(r))

		resp := rootKeyCreated{rootKeyOut: rootKeyToOut(row)}
		if mode == "generate" {
			resp.PrivateKeyHex = generatedHex
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

func activateRootKey(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := d.Store.SetActiveRootKey(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONErr(w, http.StatusNotFound, "root key not found")
				return
			}
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "activate", "root-key", id.String(), "", clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteRootKey(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := d.Store.DeleteRootKey(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONErr(w, http.StatusNotFound, "root key not found")
				return
			}
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "delete", "root-key", id.String(), "", clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- license issuance -------------------------------------------------------

type licenseIssueIn struct {
	Customer     string            `json:"customer"`
	Organization string            `json:"organization,omitempty"`
	POCName      string            `json:"poc_name,omitempty"`
	POCEmail     string            `json:"poc_email,omitempty"`
	Tier         string            `json:"tier"`
	MaxTracks    int               `json:"max_tracks,omitempty"`
	// Attributes is a free-form map of CNAK-specific knobs (numeric limits,
	// feature flags, etc.) the issued license should carry. Keys/values are
	// strings on the wire so the UI can collect any value; downstream callers
	// parse them as needed (e.g. strconv.Atoi for "max_tracks"). Empty keys
	// and empty values are stripped before signing.
	Attributes map[string]string `json:"attributes,omitempty"`
	Duration   string            `json:"duration,omitempty"`   // "365d", "2y", "never"
	ExpiresAt  string            `json:"expires_at,omitempty"` // RFC3339; takes precedence over Duration
	RootKeyID  *uuid.UUID        `json:"root_key_id,omitempty"`
	LicenseID  string            `json:"license_id,omitempty"`
}

type licenseIssueOut struct {
	License licenseDTO `json:"license"`
	LicBlob string     `json:"lic_blob"`
}

func issueLicense(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in licenseIssueIn
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		if in.Customer == "" || in.Tier == "" {
			writeJSONErr(w, http.StatusBadRequest, "customer and tier are required")
			return
		}
		// Normalize attributes: drop blank keys/values so the signed payload
		// stays tidy. If max_tracks was supplied at the top level, mirror it
		// into the attributes map so downstream callers can read all knobs
		// uniformly from one place.
		attrs := map[string]string{}
		for k, v := range in.Attributes {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k == "" || v == "" {
				continue
			}
			attrs[k] = v
		}
		if in.MaxTracks > 0 {
			if _, ok := attrs["max_tracks"]; !ok {
				attrs["max_tracks"] = strconv.Itoa(in.MaxTracks)
			}
		}
		if len(attrs) == 0 {
			attrs = nil
		}
		switch in.Tier {
		case cnaklicense.TierTrial, cnaklicense.TierProfessional, cnaklicense.TierEnterprise:
		default:
			writeJSONErr(w, http.StatusBadRequest, `tier must be one of "trial", "professional", "enterprise"`)
			return
		}

		// Resolve signing key: explicit root_key_id or the active one.
		var rk *store.RootKey
		var err error
		if in.RootKeyID != nil {
			rk, err = d.Store.GetRootKey(r.Context(), *in.RootKeyID)
			if err != nil {
				writeJSONErr(w, http.StatusBadRequest, "root key not found")
				return
			}
			if !rk.HasPrivateKey() {
				writeJSONErr(w, http.StatusBadRequest, "selected root key is verify-only (no private key on file)")
				return
			}
		} else {
			rk, err = d.Store.GetActiveSigningKey(r.Context())
			if err != nil {
				writeJSONErr(w, http.StatusBadRequest, "no active root key; activate one in Admin → Root Keys first")
				return
			}
		}

		privBytes, err := d.Crypto.Open(rk.PrivateKeyEnc)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "unwrap private key: "+err.Error())
			return
		}
		if len(privBytes) != ed25519.PrivateKeySize {
			writeJSONErr(w, http.StatusInternalServerError, "unwrapped key wrong size")
			return
		}
		priv := ed25519.PrivateKey(privBytes)

		now := time.Now().UTC()
		licID := strings.TrimSpace(in.LicenseID)
		if licID == "" {
			licID = fmt.Sprintf("lic_%d", now.Unix())
		}
		lic := &cnaklicense.License{
			ID:           licID,
			Customer:     in.Customer,
			Organization: in.Organization,
			POCName:      in.POCName,
			POCEmail:     in.POCEmail,
			Tier:         in.Tier,
			MaxTracks:    in.MaxTracks,
			Attributes:   attrs,
			IssuedAt:     now.Format(time.RFC3339),
		}

		// Expiry resolution: ExpiresAt wins over Duration; "never"/"" = perpetual.
		switch {
		case in.ExpiresAt != "":
			t, perr := time.Parse(time.RFC3339, in.ExpiresAt)
			if perr != nil {
				writeJSONErr(w, http.StatusBadRequest, "expires_at must be RFC3339")
				return
			}
			lic.ExpiresAt = t.UTC().Format(time.RFC3339)
		case in.Duration != "":
			s := strings.ToLower(strings.TrimSpace(in.Duration))
			if s == "never" || s == "0" || s == "infinite" {
				lic.ExpiresAt = ""
			} else {
				d, perr := parseLicenseDuration(s)
				if perr != nil {
					writeJSONErr(w, http.StatusBadRequest, "duration: "+perr.Error())
					return
				}
				lic.ExpiresAt = now.Add(d).Format(time.RFC3339)
			}
		default:
			writeJSONErr(w, http.StatusBadRequest, `provide either "duration" (e.g. "365d", "2y", "never") or "expires_at"`)
			return
		}

		blob, err := cnaklicense.IssueLicense(priv, lic)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "sign license: "+err.Error())
			return
		}

		row := &store.License{
			ID:           uuid.New(),
			LicenseID:    lic.ID,
			Customer:     lic.Customer,
			Organization: lic.Organization,
			Tier:         lic.Tier,
			LicBlob:      blob,
		}
		if exp, ok := parseLicenseExpiry(lic); ok {
			row.ExpiresAt = &exp
		}
		if err := d.Store.InsertLicense(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "issue", "license", row.ID.String(), row.LicenseID, clientIP(r))

		writeJSON(w, http.StatusCreated, licenseIssueOut{
			License: licenseToDTO(row),
			LicBlob: blob,
		})
	}
}

// parseLicenseDuration mirrors cnaklic/cmd/issue.parseDuration. Accepted forms:
// "365d", "2y", or any Go time.ParseDuration string ("24h"). Capped at 100y.
// Bounds-check days/years before multiplying out — large inputs would overflow
// the int64 nanosecond representation of time.Duration and silently wrap.
func parseLicenseDuration(s string) (time.Duration, error) {
	const (
		maxDays           = 100 * 365
		maxYears          = 100
		maxLicenseDuration = time.Duration(maxDays) * 24 * time.Hour
	)
	var d time.Duration
	switch {
	case strings.HasSuffix(s, "d"):
		var days int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &days); err != nil {
			return 0, fmt.Errorf("invalid days: %q", s)
		}
		if days <= 0 {
			return 0, fmt.Errorf("must be positive")
		}
		if days > maxDays {
			return 0, fmt.Errorf("exceeds maximum of 100 years")
		}
		d = time.Duration(days) * 24 * time.Hour
	case strings.HasSuffix(s, "y"):
		var years int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "y"), "%d", &years); err != nil {
			return 0, fmt.Errorf("invalid years: %q", s)
		}
		if years <= 0 {
			return 0, fmt.Errorf("must be positive")
		}
		if years > maxYears {
			return 0, fmt.Errorf("exceeds maximum of 100 years")
		}
		d = time.Duration(years) * 365 * 24 * time.Hour
	default:
		var err error
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, err
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	if d > maxLicenseDuration {
		return 0, fmt.Errorf("exceeds maximum of 100 years")
	}
	return d, nil
}
