package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
)

// catalogCredentialDTO is the per-license credential metadata returned by
// GET /catalog/api/credential. It deliberately omits SecretHash and any
// other sensitive field — the plaintext secret is returned exactly once via
// POST /catalog/api/credential/rotate.
type catalogCredentialDTO struct {
	LicenseID  string `json:"license_id"`
	TokenID    string `json:"token_id"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

func toCredentialDTO(licenseID uuid.UUID, ct *store.CustomerToken) catalogCredentialDTO {
	out := catalogCredentialDTO{
		LicenseID: licenseID.String(),
		TokenID:   ct.TokenID,
		CreatedAt: ct.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if ct.LastUsedAt != nil {
		out.LastUsedAt = ct.LastUsedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if ct.ExpiresAt != nil {
		out.ExpiresAt = ct.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return out
}

// rotateResponse is the body of POST /catalog/api/credential/rotate. The
// Secret field is plaintext, present exactly once, and must never be logged
// or written to the audit table.
type rotateResponse struct {
	LicenseID string `json:"license_id"`
	TokenID   string `json:"token_id"`
	Secret    string `json:"secret"`
	// FullCredential is `<token_id>:<secret>` — the docker login password.
	FullCredential string `json:"full_credential"`
}

// requireLicenseIDQuery parses a `license_id` query/body parameter and
// verifies the session is authorized for that license. Returns the resolved
// store.License on success; writes the HTTP response and returns nil on any
// failure path.
func requireLicenseIDForSession(d CatalogDeps, w http.ResponseWriter, r *http.Request, rawID string) *store.License {
	id := catalogFromCtx(r.Context())
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		writeJSONErr(w, http.StatusBadRequest, "license_id is required")
		return nil
	}
	wanted, err := uuid.Parse(rawID)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "license_id must be a uuid")
		return nil
	}
	licenses, err := d.listLicensesForSession(r.Context(), id)
	if err != nil {
		writeJSONErr(w, http.StatusForbidden, err.Error())
		return nil
	}
	for i := range licenses {
		if licenses[i].ID == wanted {
			return &licenses[i]
		}
	}
	writeJSONErr(w, http.StatusForbidden, "not authorized for that license")
	return nil
}

// handleCatalogGetCredential returns the active credential metadata for the
// caller-supplied license_id. Returns 404 when no active credential exists
// (so the UI can show a "Create credential" CTA).
func handleCatalogGetCredential(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lic := requireLicenseIDForSession(d, w, r, r.URL.Query().Get("license_id"))
		if lic == nil {
			return
		}
		ct, err := d.Store.ListActiveCustomerTokenForLicense(r.Context(), lic.ID)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{
				"license_id": lic.ID.String(),
				"token_id":   nil,
			})
			return
		}
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "credential lookup: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toCredentialDTO(lic.ID, ct))
	}
}

// rotateRequest is the body of POST /catalog/api/credential/rotate.
type rotateRequest struct {
	LicenseID string `json:"license_id"`
}

// handleCatalogRotateCredential atomically revokes the active credential for
// the caller's license (if any) and mints a new one. Returns the plaintext
// secret exactly once with strong no-cache headers. The session cookie is
// cleared for Basic-auth-issued sessions so the UI re-logins with the new
// secret; OIDC/Dex sessions keep their cookie.
func handleCatalogRotateCredential(d CatalogDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rotateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		lic := requireLicenseIDForSession(d, w, r, req.LicenseID)
		if lic == nil {
			return
		}
		id := catalogFromCtx(r.Context())

		gen, err := auth.GenerateCustomerToken()
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "generate credential")
			return
		}
		secretHash, err := auth.HashSecret(gen.Secret)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "hash credential")
			return
		}
		description := "self-rotate"
		if id.Impersonator != "" {
			description = "rotate-by-" + id.Impersonator
		}
		newRowID, err := d.Store.RotateCustomerTokenForLicense(
			r.Context(), lic.ID, nil, description, gen.TokenID, secretHash,
		)
		if err != nil {
			if errors.Is(err, store.ErrRotateConcurrent) {
				writeJSONErr(w, http.StatusConflict, "rotation in progress, retry")
				return
			}
			if errors.Is(err, store.ErrNotFound) {
				writeJSONErr(w, http.StatusNotFound, "license not found")
				return
			}
			writeJSONErr(w, http.StatusInternalServerError, "rotate: "+err.Error())
			return
		}

		// Invalidate any cached row-revocation state so the prior JWT stops
		// working immediately (the old row is now revoked, the new row is
		// active under a new UUID).
		if d.Revoker != nil {
			d.Revoker.BumpEpoch()
		}

		// Audit: actor = real customer for self-rotate, admin for impersonation.
		// Resource id is the license row UUID; resource name is the new public
		// token_id (which is safe to log). NEVER log the plaintext secret.
		actor := id.TokenID
		if id.Impersonator != "" {
			actor = id.Impersonator
		}
		d.Auditor.Log(audit.AuditEvent{
			Username:     actor,
			Action:       "rotate",
			ResourceType: "customer-token",
			ResourceID:   lic.ID.String(),
			ResourceName: gen.TokenID,
			IPAddress:    clientIP(r),
			Status:       "success",
		})

		// Clear the catalog cookie when the session is itself a Basic-auth
		// session bound to the (now-revoked) old token row — the cookie no
		// longer maps to a live token. Dex/OIDC sessions are not bound to a
		// customer_tokens row (TokenRowID == uuidNil) and keep their cookie.
		// newRowID is the freshly-inserted row's UUID; the prior row (if any)
		// is by definition the one in the session.
		_ = newRowID
		if id.TokenRowID != uuidNil {
			d.Sessions.Clear(w)
		}

		// Strong no-cache so the plaintext doesn't get stored anywhere on the
		// path from this process back to the browser.
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Vary", "Cookie")
		writeJSON(w, http.StatusOK, rotateResponse{
			LicenseID:      lic.ID.String(),
			TokenID:        gen.TokenID,
			Secret:         gen.Secret,
			FullCredential: gen.FullCredential,
		})
	}
}
