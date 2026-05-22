package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	cnaklicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/metrics"
	agoidc "github.com/cnak-us/artifact-gateway/oidc"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// AdminDeps wires the admin REST handlers.
type AdminDeps struct {
	Store        store.DataStore
	Crypto       *auth.Crypto
	Signer       *auth.JWTSigner
	Verifier     license.Verifier
	Auditor      *audit.Auditor
	Sessions     *agoidc.Manager
	OIDC         *agoidc.HandlerDeps
	OIDCRegistry *agoidc.Registry // for Reload() after mutations
	Metrics      *metrics.Collector
	Cfg          *config.Config
	Logger       *slog.Logger
	// Upstream is needed by probes for bucket-C (issuer-mint) Kinds — they
	// reuse the shared IssuerMintAuthenticator + its KEK plumbing. Optional;
	// nil disables ecr/gar/acr-aad probes.
	Upstream *Upstream
	// CatalogSessions is the same Manager as CatalogDeps.Sessions; an admin
	// uses it to mint a customer-flavored cookie when "viewing as customer"
	// without affecting their admin session.
	CatalogSessions *agoidc.Manager
}

// MountAdmin mounts the /api/v1/* surface onto r.
//
// Routes split into three families:
//   - auth: login/logout/me/OIDC start+callback — unauthenticated
//   - oidc-providers: admin-only, but creating one requires the bootstrap user
//   - everything else: admin-only via the Sessions middleware
func MountAdmin(r chi.Router, d AdminDeps) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", handleLogin(d))
			r.Post("/logout", handleLogout(d))
			r.With(d.Sessions.RequireAdmin).Get("/me", handleMe(d))
			r.Get("/oidc-providers", handlePublicOIDCProviders(d))
			r.Get("/oidc/{provider}/start", handleOIDCStart(d))
			r.Get("/oidc/{provider}/callback", handleOIDCCallback(d))
			r.Get("/config", handleAuthConfig(d))
		})

		r.Group(func(r chi.Router) {
			r.Use(d.Sessions.RequireAdmin)

			r.Route("/upstream-credentials", func(r chi.Router) {
				r.Get("/", listUpstreamCreds(d))
				r.Post("/", createUpstreamCred(d))
				r.Post("/{id}/test", testUpstreamCred(d))
				r.Delete("/{id}", deleteUpstreamCred(d))
			})

			r.Route("/packages", func(r chi.Router) {
				r.Get("/", listPackages(d))
				r.Post("/", createPackage(d))
				r.Get("/{id}", getPackage(d))
				r.Patch("/{id}", patchPackage(d))
				r.Post("/{id}/probe", probePackage(d))
				r.Delete("/{id}", deletePackage(d))
				r.Get("/{id}/containers", listPackageContainers(d))
				r.Post("/{id}/containers", upsertPackageContainer(d))
				r.Delete("/{id}/containers/{alias}", deletePackageContainer(d))
			})

			r.Route("/licenses", func(r chi.Router) {
				r.Get("/", listLicenses(d))
				r.Post("/", createLicense(d))
				r.Post("/issue", issueLicense(d))
				r.Get("/{id}", getLicense(d))
				r.Delete("/{id}", revokeLicense(d))
				r.Get("/{id}/grants", listGrants(d))
				r.Put("/{id}/grants", putGrants(d))
				r.Get("/{id}/contacts", listContacts(d))
				r.Post("/{id}/contacts", addContact(d))
				r.Delete("/{id}/contacts/{email}", removeContact(d))
			})

			r.Route("/root-keys", func(r chi.Router) {
				r.Get("/", listRootKeys(d))
				r.Post("/", createRootKey(d))
				r.Post("/{id}/activate", activateRootKey(d))
				r.Delete("/{id}", deleteRootKey(d))
			})

			r.Route("/customer-tokens", func(r chi.Router) {
				r.Get("/", listCustomerTokens(d))
				r.Post("/", createCustomerToken(d))
				r.Delete("/{id}", revokeCustomerToken(d))
				r.Get("/{id}/preview", previewCustomerToken(d))
			})

			r.Route("/oidc-providers", func(r chi.Router) {
				r.Get("/", listOIDCProviders(d))
				r.Post("/", createOIDCProvider(d))
				r.Delete("/{id}", deleteOIDCProvider(d))
			})

			r.Get("/audit-events", listAuditEvents(d))

			r.Route("/config", func(r chi.Router) {
				r.Post("/apply", handleConfigApply(d))
				r.Get("/export", handleConfigExport(d))
			})

			r.Route("/metrics", func(r chi.Router) {
				r.Get("/catalog", listMetricsCatalog(d))
				r.Get("/series", getMetricsSeries(d))
			})

			r.Route("/branding", func(r chi.Router) {
				r.Get("/", handleGetBranding(d))
				r.Put("/", handlePutBranding(d))
			})

			// Admin view-switcher: mint/clear the customer-cookie that lets
			// the same browser tab use /catalog as the chosen license, while
			// the ag_admin_session cookie stays untouched.
			r.Post("/view-as-customer", handleViewAsCustomer(d))
			r.Post("/end-impersonation", handleEndImpersonation(d))
		})
	})

	// Public bootstrap endpoint: the UI loader fetches this before the user
	// signs in so unauthenticated catalog visitors get the right brand. Lives
	// outside the /api/v1 admin surface on purpose — sibling to bundle/static.
	r.Get("/api/branding", handlePublicBranding(d))
}

// --- auth -------------------------------------------------------------------

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type meResp struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	// CanCustomer is true when this admin also has an ag_customer_session
	// minted during the Dex auto-flow (i.e. their email is on at least one
	// license_contacts row). Drives the "Catalog" link in the admin top bar.
	CanCustomer bool `json:"can_customer,omitempty"`
}

func handleLogin(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body loginReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" || body.Password == "" {
			writeJSONErr(w, http.StatusBadRequest, "email and password required")
			return
		}
		ip := clientIP(r)
		emailLower := strings.ToLower(strings.TrimSpace(body.Email))

		// Static admins from config short-circuit the DB. Constant-time compare
		// to avoid leaking length/prefix of configured passwords.
		if pw, ok := d.Cfg.StaticAdmins[emailLower]; ok {
			if subtle.ConstantTimeCompare([]byte(pw), []byte(body.Password)) == 1 {
				if err := d.Sessions.Issue(w, agoidc.Session{
					UserID: uuid.Nil, Email: emailLower, Role: "admin",
				}); err != nil {
					writeJSONErr(w, http.StatusInternalServerError, "session issue failed")
					return
				}
				metrics.AdminLoginsTotal.WithLabelValues("static", "success").Inc()
				d.Auditor.LogAdminLogin(emailLower, "static", ip, "success")
				writeJSON(w, http.StatusOK, meResp{UserID: uuid.Nil.String(), Email: emailLower, Role: "admin"})
				return
			}
			metrics.AdminLoginsTotal.WithLabelValues("static", "denied").Inc()
			d.Auditor.LogAdminLogin(emailLower, "static", ip, "denied")
			writeJSONErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		// Static admins from the DB (written by the declarative manifest)
		// short-circuit the regular users table the same way the env-var
		// ones do. Bcrypt verification handles constant-time comparison.
		if sa, err := d.Store.GetStaticAdminByEmail(r.Context(), emailLower); err == nil {
			if verr := auth.VerifyPassword(sa.PasswordHash, body.Password); verr == nil {
				if err := d.Sessions.Issue(w, agoidc.Session{
					UserID: uuid.Nil, Email: emailLower, Role: "admin",
				}); err != nil {
					writeJSONErr(w, http.StatusInternalServerError, "session issue failed")
					return
				}
				metrics.AdminLoginsTotal.WithLabelValues("static-db", "success").Inc()
				d.Auditor.LogAdminLogin(emailLower, "static-db", ip, "success")
				writeJSON(w, http.StatusOK, meResp{UserID: uuid.Nil.String(), Email: emailLower, Role: "admin"})
				return
			}
			metrics.AdminLoginsTotal.WithLabelValues("static-db", "denied").Inc()
			d.Auditor.LogAdminLogin(emailLower, "static-db", ip, "denied")
			writeJSONErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		user, err := d.Store.GetUserByEmail(r.Context(), emailLower)
		if err != nil || user.PasswordHash == "" {
			metrics.AdminLoginsTotal.WithLabelValues("password", "denied").Inc()
			d.Auditor.LogAdminLogin(emailLower, "password", ip, "denied")
			writeJSONErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if user.DisabledAt != nil {
			metrics.AdminLoginsTotal.WithLabelValues("password", "denied").Inc()
			d.Auditor.LogAdminLogin(body.Email, "password", ip, "disabled")
			writeJSONErr(w, http.StatusForbidden, "account disabled")
			return
		}
		if err := auth.VerifyPassword(user.PasswordHash, body.Password); err != nil {
			metrics.AdminLoginsTotal.WithLabelValues("password", "denied").Inc()
			d.Auditor.LogAdminLogin(body.Email, "password", ip, "denied")
			writeJSONErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if err := d.Sessions.Issue(w, agoidc.Session{UserID: user.ID, Email: user.Email, Role: user.Role}); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "session issue failed")
			return
		}
		metrics.AdminLoginsTotal.WithLabelValues("password", "success").Inc()
		d.Auditor.LogAdminLogin(body.Email, "password", ip, "success")
		writeJSON(w, http.StatusOK, meResp{UserID: user.ID.String(), Email: user.Email, Role: user.Role})
	}
}

func handleLogout(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.Sessions.Clear(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleMe(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := agoidc.SessionFrom(r.Context())
		if s == nil {
			writeJSONErr(w, http.StatusUnauthorized, "no session")
			return
		}
		writeJSON(w, http.StatusOK, meResp{
			UserID:      s.UserID.String(),
			Email:       s.Email,
			Role:        s.Role,
			CanCustomer: s.CanCustomer,
		})
	}
}

func handleOIDCStart(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.OIDC == nil {
			writeJSONErr(w, http.StatusNotImplemented, "oidc not configured")
			return
		}
		provider := chi.URLParam(r, "provider")
		switch r.URL.Query().Get("flow") {
		case "auto":
			d.OIDC.StartAuto(w, r, provider)
		default:
			d.OIDC.Start(w, r, provider)
		}
	}
}

func handleOIDCCallback(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.OIDC == nil {
			writeJSONErr(w, http.StatusNotImplemented, "oidc not configured")
			return
		}
		d.OIDC.Callback(w, r, chi.URLParam(r, "provider"))
	}
}

// handleAuthConfig returns the default OIDC provider info for the unauthenticated
// login page. Returns empty strings when cfg.OIDCDefaultProvider is empty or the
// named provider is not enabled in the registry.
func handleAuthConfig(d AdminDeps) http.HandlerFunc {
	type authConfigResp struct {
		DefaultProvider    string `json:"default_provider"`
		DefaultDisplayName string `json:"default_display_name"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		name := d.Cfg.OIDCDefaultProvider
		if name == "" {
			writeJSON(w, http.StatusOK, authConfigResp{})
			return
		}
		// Verify the provider exists and is enabled.
		rows, err := d.Store.ListOIDCProviders(r.Context())
		if err != nil {
			writeJSON(w, http.StatusOK, authConfigResp{})
			return
		}
		found := false
		for _, p := range rows {
			if p.Name == name && p.Enabled {
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusOK, authConfigResp{})
			return
		}
		writeJSON(w, http.StatusOK, authConfigResp{
			DefaultProvider:    name,
			DefaultDisplayName: strings.ToLower(name),
		})
	}
}

// --- upstream credentials ---------------------------------------------------

type upstreamCredOut struct {
	ID                    uuid.UUID  `json:"id"`
	Name                  string     `json:"name"`
	Kind                  string     `json:"kind"`
	Username              string     `json:"username"`
	PATFingerprint        string     `json:"pat_fingerprint"`
	BaseURL               string     `json:"base_url,omitempty"`
	Endpoint              string     `json:"endpoint"`
	IssuerKind            string     `json:"issuer_kind,omitempty"`
	IssuerConfig          string     `json:"issuer_config,omitempty"`
	HasCABundle           bool       `json:"has_ca_bundle"`
	InsecureSkipTLSVerify bool       `json:"insecure_skip_tls_verify,omitempty"`
	LastUsedAt            *time.Time `json:"last_used_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
}

type upstreamCredIn struct {
	Name                  string          `json:"name"`
	Kind                  string          `json:"kind"`
	Username              string          `json:"username"`
	PAT                   string          `json:"pat"`
	BaseURL               string          `json:"base_url"`
	CABundlePEM           string          `json:"ca_bundle_pem"`
	InsecureSkipTLSVerify bool            `json:"insecure_skip_tls_verify"`
	IssuerSecret          json.RawMessage `json:"issuer_secret,omitempty"`
	IssuerConfig          json.RawMessage `json:"issuer_config,omitempty"`
}

// isIssuerKind reports whether a credential Kind uses an issuer-mint
// authenticator (bucket C). These Kinds do not carry a PAT — they carry an
// IssuerSecret + IssuerConfig instead.
func isIssuerKind(kind string) bool {
	switch kind {
	case "ecr", "gar", "acr-aad":
		return true
	}
	return false
}

// issuerCloudFor maps a credential Kind to the IssuerKind column value.
// IssuerKind groups Kinds by cloud so admin tools can filter (e.g. "all
// AWS issuers") without parsing Kind strings.
func issuerCloudFor(kind string) string {
	switch kind {
	case "ecr":
		return "aws"
	case "gar":
		return "gcp"
	case "acr-aad":
		return "azure"
	}
	return ""
}

// validUpstreamCredKinds is the canonical allowlist of credential Kind
// values. Keep in sync with the upstream_credentials_kind_check constraint
// in store/schema.sql and with KIND_OPTS in the UI.
var validUpstreamCredKinds = []string{
	"ghcr", "github-api", "oci-basic",
	"dockerhub", "quay", "gitlab",
	"ecr", "gar", "acr-aad",
	"gitlab-api",
}

func isValidUpstreamCredKind(kind string) bool {
	for _, k := range validUpstreamCredKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func upstreamCredOutFrom(c *store.UpstreamCredential) upstreamCredOut {
	cfg := ""
	if len(c.IssuerConfigJSON) > 0 && string(c.IssuerConfigJSON) != "{}" {
		cfg = string(c.IssuerConfigJSON)
	}
	return upstreamCredOut{
		ID:                    c.ID,
		Name:                  c.Name,
		Kind:                  c.Kind,
		Username:              c.Username,
		PATFingerprint:        c.PATFingerprint,
		BaseURL:               c.BaseURL,
		Endpoint:              effectiveHost(c),
		IssuerKind:            c.IssuerKind,
		IssuerConfig:          cfg,
		HasCABundle:           c.CABundlePEM != "",
		InsecureSkipTLSVerify: c.InsecureSkipTLSVerify,
		LastUsedAt:            c.LastUsedAt,
		CreatedAt:             c.CreatedAt,
	}
}

func listUpstreamCreds(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Store.ListUpstreamCredentials(r.Context())
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]upstreamCredOut, 0, len(rows))
		for i := range rows {
			out = append(out, upstreamCredOutFrom(&rows[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func createUpstreamCred(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in upstreamCredIn
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			writeJSONErr(w, http.StatusBadRequest, "name required")
			return
		}
		if in.Kind == "" {
			in.Kind = "ghcr"
		}
		if !isValidUpstreamCredKind(in.Kind) {
			writeJSONErr(w, http.StatusBadRequest, "invalid kind; valid: "+strings.Join(validUpstreamCredKinds, ", "))
			return
		}
		issuer := isIssuerKind(in.Kind)
		if issuer {
			if len(in.IssuerSecret) == 0 {
				writeJSONErr(w, http.StatusBadRequest, "issuer_secret required for "+in.Kind)
				return
			}
		} else if in.Username == "" || in.PAT == "" {
			writeJSONErr(w, http.StatusBadRequest, "username and pat required for "+in.Kind)
			return
		}
		switch in.Kind {
		case "oci-basic", "gitlab", "gar", "acr-aad", "gitlab-api":
			if in.BaseURL == "" {
				writeJSONErr(w, http.StatusBadRequest, "base_url required for "+in.Kind)
				return
			}
		case "ghcr", "dockerhub":
			if in.BaseURL != "" {
				writeJSONErr(w, http.StatusBadRequest, "base_url must be empty for "+in.Kind+" (host is pinned)")
				return
			}
		}

		row := &store.UpstreamCredential{
			ID:                    uuid.New(),
			Name:                  in.Name,
			Kind:                  in.Kind,
			Username:              in.Username,
			BaseURL:               strings.TrimRight(in.BaseURL, "/"),
			CABundlePEM:           in.CABundlePEM,
			InsecureSkipTLSVerify: in.InsecureSkipTLSVerify,
		}
		if issuer {
			sealed, err := d.Crypto.Seal(in.IssuerSecret)
			if err != nil {
				writeJSONErr(w, http.StatusInternalServerError, "seal issuer secret failed")
				return
			}
			row.IssuerKind = issuerCloudFor(in.Kind)
			row.IssuerSecretEnc = sealed
			row.IssuerConfigJSON = []byte(in.IssuerConfig)
			if len(row.IssuerConfigJSON) == 0 {
				row.IssuerConfigJSON = []byte(`{}`)
			}
		} else {
			sealed, err := d.Crypto.Seal([]byte(in.PAT))
			if err != nil {
				writeJSONErr(w, http.StatusInternalServerError, "seal failed")
				return
			}
			row.PATEnc = sealed
			row.PATFingerprint = d.Crypto.Fingerprint([]byte(in.PAT))
		}
		if err := d.Store.InsertUpstreamCredential(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "create", "upstream-credential", row.ID.String(), row.Name, clientIP(r))
		writeJSON(w, http.StatusCreated, upstreamCredOutFrom(row))
	}
}

func deleteUpstreamCred(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := d.Store.DeleteUpstreamCredential(r.Context(), id); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "delete", "upstream-credential", id.String(), "", clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- packages ---------------------------------------------------------------

type packageDTO struct {
	ID                    uuid.UUID `json:"id"`
	Slug                  string    `json:"slug"`
	Path                  string    `json:"path"`
	UpstreamRepo          string    `json:"upstream_repo"`
	UpstreamCredentialID  uuid.UUID `json:"upstream_credential_id"`
	Kind                  string    `json:"kind"`
	DisplayName           string    `json:"display_name"`
	Description           string    `json:"description,omitempty"`
	ReleaseNotesURL       string    `json:"release_notes_url,omitempty"`
	InstallInstructionsMD string    `json:"install_instructions_md,omitempty"`
	Source                string    `json:"source,omitempty"`
	GitHubRepo            string    `json:"github_repo,omitempty"`
	ReleasePattern        string    `json:"release_pattern,omitempty"`
	AssetPattern          string    `json:"asset_pattern,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func packageToDTO(p *store.Package) packageDTO {
	return packageDTO{
		ID: p.ID, Slug: p.Slug, Path: p.Path, UpstreamRepo: p.UpstreamRepo,
		UpstreamCredentialID: p.UpstreamCredentialID, Kind: p.Kind,
		DisplayName: p.DisplayName, Description: p.Description,
		ReleaseNotesURL: p.ReleaseNotesURL, InstallInstructionsMD: p.InstallInstructionsMD,
		Source: p.Source, GitHubRepo: p.GitHubRepo,
		ReleasePattern: p.ReleasePattern, AssetPattern: p.AssetPattern,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func listPackages(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Store.ListPackages(r.Context())
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]packageDTO, 0, len(rows))
		for i := range rows {
			out = append(out, packageToDTO(&rows[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func getPackage(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		p, err := d.Store.GetPackage(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, packageToDTO(p))
	}
}

func createPackage(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in packageDTO
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil ||
			in.Slug == "" || in.Path == "" || in.UpstreamRepo == "" || in.Kind == "" {
			writeJSONErr(w, http.StatusBadRequest, "slug, path, upstream_repo, kind required")
			return
		}
		if in.UpstreamCredentialID == uuid.Nil {
			writeJSONErr(w, http.StatusBadRequest, "upstream_credential_id required")
			return
		}
		if in.Source == "github-release" {
			if in.GitHubRepo == "" {
				writeJSONErr(w, http.StatusBadRequest, "github_repo required when source=github-release")
				return
			}
			if in.ReleasePattern == "" {
				in.ReleasePattern = "latest"
			}
			if in.AssetPattern == "" {
				in.AssetPattern = "*"
			}
		}
		row := &store.Package{
			ID:                    uuid.New(),
			Slug:                  in.Slug,
			Path:                  in.Path,
			UpstreamRepo:          in.UpstreamRepo,
			UpstreamCredentialID:  in.UpstreamCredentialID,
			Kind:                  in.Kind,
			DisplayName:           in.DisplayName,
			Description:           in.Description,
			ReleaseNotesURL:       in.ReleaseNotesURL,
			InstallInstructionsMD: in.InstallInstructionsMD,
			Source:                in.Source,
			GitHubRepo:            in.GitHubRepo,
			ReleasePattern:        in.ReleasePattern,
			AssetPattern:          in.AssetPattern,
		}
		if err := d.Store.InsertPackage(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "create", "package", row.ID.String(), row.Slug, clientIP(r))
		writeJSON(w, http.StatusCreated, packageToDTO(row))
	}
}

func patchPackage(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		existing, err := d.Store.GetPackage(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, err.Error())
			return
		}
		var in packageDTO
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		// Patch: only mutate fields actually supplied.
		if in.DisplayName != "" {
			existing.DisplayName = in.DisplayName
		}
		if in.Description != "" {
			existing.Description = in.Description
		}
		if in.ReleaseNotesURL != "" {
			existing.ReleaseNotesURL = in.ReleaseNotesURL
		}
		if in.InstallInstructionsMD != "" {
			existing.InstallInstructionsMD = in.InstallInstructionsMD
		}
		if in.UpstreamRepo != "" {
			existing.UpstreamRepo = in.UpstreamRepo
		}
		if in.UpstreamCredentialID != uuid.Nil {
			existing.UpstreamCredentialID = in.UpstreamCredentialID
		}
		if in.Source != "" {
			existing.Source = in.Source
		}
		if in.GitHubRepo != "" {
			existing.GitHubRepo = in.GitHubRepo
		}
		if in.ReleasePattern != "" {
			existing.ReleasePattern = in.ReleasePattern
		}
		if in.AssetPattern != "" {
			existing.AssetPattern = in.AssetPattern
		}
		if in.Kind != "" {
			existing.Kind = in.Kind
		}
		if err := d.Store.UpdatePackage(r.Context(), existing); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "update", "package", existing.ID.String(), existing.Slug, clientIP(r))
		writeJSON(w, http.StatusOK, packageToDTO(existing))
	}
}

func deletePackage(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := d.Store.DeletePackage(r.Context(), id); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "delete", "package", id.String(), "", clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- licenses ---------------------------------------------------------------

type licenseDTO struct {
	ID           uuid.UUID  `json:"id"`
	LicenseID    string     `json:"license_id"`
	Customer     string     `json:"customer"`
	Organization string     `json:"organization,omitempty"`
	Tier         string     `json:"tier"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type licenseIn struct {
	LicBlob string `json:"lic_blob"`
}

func licenseToDTO(l *store.License) licenseDTO {
	return licenseDTO{
		ID: l.ID, LicenseID: l.LicenseID, Customer: l.Customer,
		Organization: l.Organization, Tier: l.Tier, ExpiresAt: l.ExpiresAt,
		RevokedAt: l.RevokedAt, CreatedAt: l.CreatedAt,
	}
}

func listLicenses(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Store.ListLicenses(r.Context())
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]licenseDTO, 0, len(rows))
		for i := range rows {
			out = append(out, licenseToDTO(&rows[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func getLicense(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		l, err := d.Store.GetLicense(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, licenseToDTO(l))
	}
}

func createLicense(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in licenseIn
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.LicBlob == "" {
			writeJSONErr(w, http.StatusBadRequest, "lic_blob required")
			return
		}
		parsed, err := d.Verifier.VerifyLicenseBlob(in.LicBlob)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "license invalid: "+err.Error())
			return
		}
		row := &store.License{
			ID:           uuid.New(),
			LicenseID:    parsed.ID,
			Customer:     parsed.Customer,
			Organization: parsed.Organization,
			Tier:         parsed.Tier,
			LicBlob:      in.LicBlob,
		}
		if exp, ok := parseLicenseExpiry(parsed); ok {
			row.ExpiresAt = &exp
		}
		if err := d.Store.InsertLicense(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "create", "license", row.ID.String(), row.LicenseID, clientIP(r))
		writeJSON(w, http.StatusCreated, licenseToDTO(row))
	}
}

func revokeLicense(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := d.Store.RevokeLicense(r.Context(), id); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "revoke", "license", id.String(), "", clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

type grantsBody struct {
	// New shape (what the UI sends): a list of per-package grants. Each
	// entry's actions default to ["pull"] when empty.
	Grants []grantEntry `json:"grants"`
	// Legacy shape: a flat list of package IDs sharing a single actions set.
	// Honored only when Grants is empty.
	PackageIDs []uuid.UUID `json:"package_ids"`
	Actions    []string    `json:"actions"`
}

type grantEntry struct {
	PackageID uuid.UUID `json:"package_id"`
	Actions   []string  `json:"actions"`
}

func listGrants(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		rows, err := d.Store.ListGrantsForLicense(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Return the same shape the UI sends on PUT: {grants: [{package_id, actions}]}
		out := make([]grantEntry, 0, len(rows))
		for _, g := range rows {
			out = append(out, grantEntry{PackageID: g.PackageID, Actions: g.Actions})
		}
		writeJSON(w, http.StatusOK, map[string]any{"grants": out})
	}
}

func putGrants(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		var body grantsBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}

		// Normalize either shape into (packageIDs, actions). The store layer
		// applies the same actions to every package in the set — fine for v1
		// since granted actions are always ["pull"].
		var packageIDs []uuid.UUID
		actions := body.Actions
		if len(body.Grants) > 0 {
			seen := make(map[uuid.UUID]struct{}, len(body.Grants))
			for _, g := range body.Grants {
				if g.PackageID == uuid.Nil {
					continue
				}
				if _, dup := seen[g.PackageID]; dup {
					continue
				}
				seen[g.PackageID] = struct{}{}
				packageIDs = append(packageIDs, g.PackageID)
				if len(actions) == 0 && len(g.Actions) > 0 {
					actions = g.Actions
				}
			}
		} else {
			packageIDs = body.PackageIDs
		}
		if len(actions) == 0 {
			actions = []string{"pull"}
		}
		// Refuse to wipe everything by accident: require an explicit empty
		// `package_ids: []` or `grants: []` to clear. If the body was parsed
		// as nil (e.g. wrong-shape JSON we didn't detect), reject.
		if packageIDs == nil && body.Grants == nil && body.PackageIDs == nil {
			writeJSONErr(w, http.StatusBadRequest, "body must include either `grants` or `package_ids` (use [] to clear)")
			return
		}

		if err := d.Store.ReplaceGrantsForLicense(r.Context(), id, packageIDs, actions); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "replace_grants", "license", id.String(), "", clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- license contacts -------------------------------------------------------

// emailRe is a loose RFC-ish email check: one or more non-@/non-space chars,
// an @, more non-@/non-space chars, a literal '.', and a TLD. Good enough to
// catch typos at the admin form; the real validation is that the operator can
// successfully sign in via OIDC with this address.
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type contactDTO struct {
	LicenseID uuid.UUID `json:"license_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func contactToDTO(c *store.LicenseContact) contactDTO {
	return contactDTO{
		LicenseID: c.LicenseID,
		Email:     c.Email,
		Name:      c.Name,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

type addContactIn struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

func listContacts(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		rows, err := d.Store.ListContactsForLicense(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]contactDTO, 0, len(rows))
		for i := range rows {
			out = append(out, contactToDTO(&rows[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func addContact(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		var in addContactIn
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		email := strings.ToLower(strings.TrimSpace(in.Email))
		if email == "" {
			writeJSONErr(w, http.StatusBadRequest, "email required")
			return
		}
		if !emailRe.MatchString(email) {
			writeJSONErr(w, http.StatusBadRequest, "invalid email format")
			return
		}
		row := &store.LicenseContact{
			LicenseID: id,
			Email:     email,
			Name:      strings.TrimSpace(in.Name),
		}
		if err := d.Store.AddContact(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "add_contact", "license-contact", id.String(), email, clientIP(r))
		writeJSON(w, http.StatusCreated, contactToDTO(row))
	}
}

func removeContact(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		rawEmail, decErr := url.PathUnescape(chi.URLParam(r, "email"))
		if decErr != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid email encoding")
			return
		}
		email := strings.ToLower(strings.TrimSpace(rawEmail))
		if email == "" {
			writeJSONErr(w, http.StatusBadRequest, "email required")
			return
		}
		if err := d.Store.RemoveContact(r.Context(), id, email); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "remove_contact", "license-contact", id.String(), email, clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- package containers ----------------------------------------------------

// containerAliasAdminRe matches the same alias shape the reconciler enforces:
// [A-Za-z0-9._-]+, no '/'. The DB CHECK enforces the no-slash invariant on
// the schema side; this regex is the friendlier rejection at the API edge.
var containerAliasAdminRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type containerDTO struct {
	PackageID    uuid.UUID `json:"package_id"`
	Alias        string    `json:"alias"`
	UpstreamRepo string    `json:"upstream_repo"`
	DisplayName  string    `json:"display_name,omitempty"`
	Source       string    `json:"source,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func containerToDTO(c *store.PackageContainer) containerDTO {
	return containerDTO{
		PackageID:    c.PackageID,
		Alias:        c.Alias,
		UpstreamRepo: c.UpstreamRepo,
		DisplayName:  c.DisplayName,
		Source:       c.Source,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
	}
}

type upsertContainerIn struct {
	Alias        string `json:"alias"`
	UpstreamRepo string `json:"upstream_repo"`
	DisplayName  string `json:"display_name"`
}

func listPackageContainers(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		rows, err := d.Store.ListContainersForPackage(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]containerDTO, 0, len(rows))
		for i := range rows {
			out = append(out, containerToDTO(&rows[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// upsertPackageContainer creates or updates a container. Rows written here
// are tagged source='' (UI-owned) so a later manifest apply won't strip them.
func upsertPackageContainer(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		var in upsertContainerIn
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		alias := strings.TrimSpace(in.Alias)
		upstream := strings.TrimSpace(in.UpstreamRepo)
		if alias == "" {
			writeJSONErr(w, http.StatusBadRequest, "alias required")
			return
		}
		if !containerAliasAdminRe.MatchString(alias) {
			writeJSONErr(w, http.StatusBadRequest, "invalid alias: only [A-Za-z0-9._-] allowed, no '/'")
			return
		}
		if upstream == "" {
			writeJSONErr(w, http.StatusBadRequest, "upstream_repo required")
			return
		}
		row := &store.PackageContainer{
			PackageID:    id,
			Alias:        alias,
			UpstreamRepo: upstream,
			DisplayName:  strings.TrimSpace(in.DisplayName),
			Source:       "",
		}
		if err := d.Store.UpsertContainer(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "upsert_container", "package-container", id.String(), alias, clientIP(r))
		writeJSON(w, http.StatusCreated, containerToDTO(row))
	}
}

// deletePackageContainer removes a container row by alias. We don't restrict
// by source: admins are trusted to manage their own data, and a manifest
// re-apply will recreate any manifest-owned row that was deleted by hand.
func deletePackageContainer(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		rawAlias, decErr := url.PathUnescape(chi.URLParam(r, "alias"))
		if decErr != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid alias encoding")
			return
		}
		alias := strings.TrimSpace(rawAlias)
		if alias == "" {
			writeJSONErr(w, http.StatusBadRequest, "alias required")
			return
		}
		if err := d.Store.DeleteContainer(r.Context(), id, alias); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "delete_container", "package-container", id.String(), alias, clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- customer tokens --------------------------------------------------------

type customerTokenOut struct {
	ID          uuid.UUID  `json:"id"`
	TokenID     string     `json:"token_id"`
	LicenseID   uuid.UUID  `json:"license_id"`
	Description string     `json:"description,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type customerTokenIssued struct {
	customerTokenOut
	Secret         string `json:"secret"`
	FullCredential string `json:"full_credential"`
}

type customerTokenIn struct {
	LicenseID   uuid.UUID  `json:"license_id"`
	Description string     `json:"description"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func customerTokenToOut(t *store.CustomerToken) customerTokenOut {
	return customerTokenOut{
		ID: t.ID, TokenID: t.TokenID, LicenseID: t.LicenseID,
		Description: t.Description, ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt,
		LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt,
	}
}

func listCustomerTokens(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var lid *uuid.UUID
		if q := r.URL.Query().Get("license_id"); q != "" {
			if id, err := uuid.Parse(q); err == nil {
				lid = &id
			}
		}
		rows, err := d.Store.ListCustomerTokens(r.Context(), lid)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]customerTokenOut, 0, len(rows))
		for i := range rows {
			out = append(out, customerTokenToOut(&rows[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func createCustomerToken(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in customerTokenIn
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.LicenseID == uuid.Nil {
			writeJSONErr(w, http.StatusBadRequest, "license_id required")
			return
		}
		// Ensure the license exists before we mint a credential against it.
		if _, err := d.Store.GetLicense(r.Context(), in.LicenseID); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "license not found")
			return
		}
		gen, err := auth.GenerateCustomerToken()
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "generate failed")
			return
		}
		hash, err := auth.HashSecret(gen.Secret)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "hash failed")
			return
		}
		s := agoidc.SessionFrom(r.Context())
		var createdBy *uuid.UUID
		if s != nil && s.UserID != uuid.Nil {
			id := s.UserID
			createdBy = &id
		}
		row := &store.CustomerToken{
			ID:          uuid.New(),
			TokenID:     gen.TokenID,
			SecretHash:  hash,
			LicenseID:   in.LicenseID,
			Description: in.Description,
			ExpiresAt:   in.ExpiresAt,
			CreatedBy:   createdBy,
		}
		if err := d.Store.InsertCustomerToken(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		d.Auditor.LogResourceMutation(actorEmail(s), "create", "customer-token", row.ID.String(), row.TokenID, clientIP(r))
		writeJSON(w, http.StatusCreated, customerTokenIssued{
			customerTokenOut: customerTokenToOut(row),
			Secret:           gen.Secret,
			FullCredential:   gen.FullCredential,
		})
	}
}

func revokeCustomerToken(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := d.Store.RevokeCustomerToken(r.Context(), id); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "revoke", "customer-token", id.String(), "", clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// previewCustomerToken returns the operator's-eye view of what this token can
// actually pull right now: the granted packages, the credential expiry, and
// the live license status (parsed from the stored .lic so a soon-to-expire
// license is visible even before the next mint attempt).
func previewCustomerToken(d AdminDeps) http.HandlerFunc {
	type licenseStatus struct {
		Licensed bool   `json:"licensed"`
		Expired  bool   `json:"expired"`
		Tier     string `json:"tier,omitempty"`
		Customer string `json:"customer,omitempty"`
	}
	type previewResp struct {
		Packages      []packageDTO  `json:"packages"`
		ExpiresAt     *time.Time    `json:"expires_at,omitempty"`
		LicenseStatus licenseStatus `json:"license_status"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		t, err := d.Store.GetCustomerToken(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "token not found")
			return
		}
		lic, err := d.Store.GetLicense(r.Context(), t.LicenseID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		pkgs, err := d.Store.GrantedPackagesForLicense(r.Context(), t.LicenseID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		pkgOut := make([]packageDTO, 0, len(pkgs))
		for i := range pkgs {
			pkgOut = append(pkgOut, packageToDTO(&pkgs[i]))
		}
		ls := licenseStatus{Tier: lic.Tier, Customer: lic.Customer}
		parsed, err := d.Verifier.VerifyLicenseBlob(lic.LicBlob)
		if err == nil {
			ls.Licensed = license.CheckActive(parsed, lic.RevokedAt, lic.LicenseID) == nil
			ls.Expired = parsed.IsExpired()
		}
		writeJSON(w, http.StatusOK, previewResp{
			Packages:      pkgOut,
			ExpiresAt:     t.ExpiresAt,
			LicenseStatus: ls,
		})
	}
}

// --- OIDC providers ---------------------------------------------------------

type oidcProviderOut struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	IssuerURL string    `json:"issuer_url"`
	ClientID  string    `json:"client_id"`
	Scopes    []string  `json:"scopes"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type oidcProviderIn struct {
	Name         string   `json:"name"`
	IssuerURL    string   `json:"issuer_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	Enabled      bool     `json:"enabled"`
}

// handlePublicOIDCProviders returns a minimal listing of *enabled* providers
// for the unauthenticated login page to render Sign-in-with buttons. No
// secrets, no issuer URLs, no client IDs — just name + display.
func handlePublicOIDCProviders(d AdminDeps) http.HandlerFunc {
	type publicProvider struct {
		Name        string `json:"name"`
		Enabled     bool   `json:"enabled"`
		DisplayName string `json:"display_name"`
		IsDefault   bool   `json:"is_default"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Store.ListOIDCProviders(r.Context())
		if err != nil {
			// Don't fail the login page if the DB is hiccupping — return empty.
			writeJSON(w, http.StatusOK, []publicProvider{})
			return
		}
		out := make([]publicProvider, 0, len(rows))
		for _, p := range rows {
			if !p.Enabled {
				continue
			}
			out = append(out, publicProvider{
				Name:        p.Name,
				Enabled:     true,
				DisplayName: strings.ToLower(p.Name),
				IsDefault:   p.Name == d.Cfg.OIDCDefaultProvider,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func listOIDCProviders(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Store.ListOIDCProviders(r.Context())
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]oidcProviderOut, 0, len(rows))
		for _, p := range rows {
			out = append(out, oidcProviderOut{
				ID: p.ID, Name: p.Name, IssuerURL: p.IssuerURL, ClientID: p.ClientID,
				Scopes: p.Scopes, Enabled: p.Enabled, CreatedAt: p.CreatedAt,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func createOIDCProvider(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in oidcProviderIn
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil ||
			in.Name == "" || in.IssuerURL == "" || in.ClientID == "" || in.ClientSecret == "" {
			writeJSONErr(w, http.StatusBadRequest, "name, issuer_url, client_id, client_secret required")
			return
		}
		sealed, err := d.Crypto.Seal([]byte(in.ClientSecret))
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "seal failed")
			return
		}
		row := &store.OIDCProvider{
			ID:              uuid.New(),
			Name:            in.Name,
			IssuerURL:       in.IssuerURL,
			ClientID:        in.ClientID,
			ClientSecretEnc: sealed,
			Scopes:          in.Scopes,
			Enabled:         in.Enabled,
		}
		if err := d.Store.InsertOIDCProvider(r.Context(), row); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Refresh the in-memory registry so /api/v1/auth/oidc/:name/start
		// works immediately (no process restart needed).
		if d.OIDCRegistry != nil {
			if rerr := d.OIDCRegistry.Reload(r.Context()); rerr != nil {
				d.Logger.Warn("oidc registry reload after create failed", "err", rerr)
			}
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "create", "oidc-provider", row.ID.String(), row.Name, clientIP(r))
		writeJSON(w, http.StatusCreated, oidcProviderOut{
			ID: row.ID, Name: row.Name, IssuerURL: row.IssuerURL, ClientID: row.ClientID,
			Scopes: row.Scopes, Enabled: row.Enabled, CreatedAt: row.CreatedAt,
		})
	}
}

func deleteOIDCProvider(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := d.Store.DeleteOIDCProvider(r.Context(), id); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if d.OIDCRegistry != nil {
			if rerr := d.OIDCRegistry.Reload(r.Context()); rerr != nil {
				d.Logger.Warn("oidc registry reload after delete failed", "err", rerr)
			}
		}
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "delete", "oidc-provider", id.String(), "", clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- audit ------------------------------------------------------------------

func listAuditEvents(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		var cursor *time.Time
		if v := r.URL.Query().Get("before"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				cursor = &t
			}
		}
		evs, err := d.Store.ListAuditEvents(r.Context(), limit, cursor)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, evs)
	}
}

// --- helpers ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

func actorEmail(s *agoidc.Session) string {
	if s == nil {
		return ""
	}
	return s.Email
}

// parseLicenseExpiry pulls the RFC3339 expiry off a parsed cnaklicense.License.
// Returns ok=false for perpetual or unparseable values.
func parseLicenseExpiry(l *cnaklicense.License) (time.Time, bool) {
	if l.ExpiresAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, l.ExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// --- view-as-customer -------------------------------------------------------
//
// An admin can preview the catalog from the perspective of a specific license
// without having to log in as a customer. This mints an ag_customer_session
// cookie bound to that license + carrying the admin's email as Impersonator.
//
// The two cookies (ag_admin_session and ag_customer_session) coexist in the
// browser — admins keep their admin privileges while previewing. Ending the
// impersonation clears only the customer cookie.

type viewAsCustomerReq struct {
	LicenseID string `json:"license_id"` // human-readable license_id, not the row UUID
}

func handleViewAsCustomer(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.CatalogSessions == nil {
			writeJSONErr(w, http.StatusNotImplemented, "catalog sessions not configured")
			return
		}
		s := agoidc.SessionFrom(r.Context())
		if s == nil {
			writeJSONErr(w, http.StatusUnauthorized, "no session")
			return
		}
		var body viewAsCustomerReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.LicenseID) == "" {
			writeJSONErr(w, http.StatusBadRequest, "license_id is required")
			return
		}
		lic, err := d.Store.GetLicenseByLicenseID(r.Context(), strings.TrimSpace(body.LicenseID))
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "license not found")
			return
		}
		if lic.RevokedAt != nil {
			writeJSONErr(w, http.StatusForbidden, "license is revoked")
			return
		}
		if err := d.CatalogSessions.Issue(w, agoidc.Session{
			UserID:       uuid.Nil,
			Email:        s.Email,
			Role:         "customer",
			LicenseID:    lic.LicenseID,
			Impersonator: s.Email,
		}); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "session issue failed")
			return
		}
		if d.Auditor != nil {
			d.Auditor.LogAdminLogin(s.Email, "view-as-customer", clientIP(r), "license:"+lic.LicenseID)
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"license_id": lic.LicenseID,
			"redirect":   "/catalog",
		})
	}
}

func handleEndImpersonation(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.CatalogSessions != nil {
			d.CatalogSessions.Clear(w)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
