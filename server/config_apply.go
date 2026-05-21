package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cnak-us/artifact-gateway/apply"
	agoidc "github.com/cnak-us/artifact-gateway/oidc"
	"github.com/cnak-us/artifact-gateway/store"
	"gopkg.in/yaml.v3"
)

// redactedSecret is the placeholder the export endpoint emits in lieu of any
// secret material. The literal string matters: cr-frontend keys off it to
// render "(re-supply on next apply)" hints in the UI.
const redactedSecret = "<redacted>"

func handleConfigApply(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		if len(raw) == 0 {
			writeJSONErr(w, http.StatusBadRequest, "empty body")
			return
		}

		mf, err := apply.Parse(raw)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := apply.Resolve(mf); err != nil {
			var miss *apply.MissingEnvError
			if errors.As(err, &miss) {
				// Surface the structured list of missing env vars so the UI
				// can render them as actionable items rather than smashing
				// them together in a string.
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":            err.Error(),
					"missing_env_refs": miss.Refs,
				})
				return
			}
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}

		opts := apply.Options{
			DryRun: queryBool(r, "dry_run"),
			Prune:  queryBool(r, "prune"),
		}

		report, err := apply.Reconcile(r.Context(), d.Store, d.Crypto, d.Verifier, mf, opts)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Refresh OIDC registry if any provider items were touched and we
		// actually wrote — otherwise dry-run wouldn't see the previous
		// providers go stale.
		if !opts.DryRun && d.OIDCRegistry != nil && anyProviderTouched(report) {
			if rerr := d.OIDCRegistry.Reload(r.Context()); rerr != nil {
				d.Logger.Warn("oidc registry reload after apply failed", "err", rerr)
			}
		}

		// Audit one event per non-noop item so the trail captures who applied
		// what. Skip noop and skip everything in dry-run (no state change to
		// audit).
		if !opts.DryRun {
			s := agoidc.SessionFrom(r.Context())
			actor := actorEmail(s)
			ip := clientIP(r)
			for _, it := range report.Items {
				if it.Action == apply.ActionNoop {
					continue
				}
				d.Auditor.LogResourceMutation(actor, "apply:"+it.Action, it.Kind, "", it.Name, ip)
			}
		}

		writeJSON(w, http.StatusOK, report)
	}
}

func handleConfigExport(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mf, err := exportManifest(r.Context(), d.Store)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out, err := yaml.Marshal(mf)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "marshal: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Content-Disposition", `attachment; filename="artifact-gateway-config.yaml"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}
}

// exportManifest reads current DB state and emits a Manifest with every
// secret field set to the literal placeholder so the operator sees field
// presence but has to re-enter the secret before next apply. This is the
// "show me what's configured" path, not a backup path.
func exportManifest(ctx context.Context, st store.DataStore) (*apply.Manifest, error) {
	mf := &apply.Manifest{
		APIVersion: apply.APIVersion,
		Kind:       apply.Kind,
		Metadata:   apply.Metadata{Name: "default"},
	}

	staticAdmins, err := st.ListStaticAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("list static admins: %w", err)
	}
	for _, sa := range staticAdmins {
		mf.Spec.StaticAdmins = append(mf.Spec.StaticAdmins, apply.StaticAdminSpec{
			Email:    sa.Email,
			Password: redactedSecret,
		})
	}

	providers, err := st.ListOIDCProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list oidc providers: %w", err)
	}
	for _, p := range providers {
		mf.Spec.OIDCProviders = append(mf.Spec.OIDCProviders, apply.OIDCProviderSpec{
			Name:         p.Name,
			IssuerURL:    p.IssuerURL,
			ClientID:     p.ClientID,
			ClientSecret: redactedSecret,
			Scopes:       p.Scopes,
			Enabled:      p.Enabled,
		})
	}

	creds, err := st.ListUpstreamCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("list upstream credentials: %w", err)
	}
	credIDToName := make(map[string]string, len(creds))
	for _, c := range creds {
		credIDToName[c.ID.String()] = c.Name
		mf.Spec.UpstreamCredentials = append(mf.Spec.UpstreamCredentials, apply.UpstreamCredentialSpec{
			Name:                  c.Name,
			Kind:                  c.Kind,
			Username:              c.Username,
			PAT:                   redactedSecret,
			BaseURL:               c.BaseURL,
			CABundlePEM:           c.CABundlePEM,
			InsecureSkipTLSVerify: c.InsecureSkipTLSVerify,
		})
	}

	pkgs, err := st.ListPackages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	pkgIDToSlug := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		pkgIDToSlug[p.ID.String()] = p.Slug
		mf.Spec.Packages = append(mf.Spec.Packages, apply.PackageSpec{
			Slug:                  p.Slug,
			Source:                p.Source,
			Path:                  p.Path,
			UpstreamRepo:          p.UpstreamRepo,
			GitHubRepo:            p.GitHubRepo,
			ReleasePattern:        p.ReleasePattern,
			AssetPattern:          p.AssetPattern,
			UpstreamCredential:    credIDToName[p.UpstreamCredentialID.String()],
			Kind:                  p.Kind,
			DisplayName:           p.DisplayName,
			Description:           p.Description,
			ReleaseNotesURL:       p.ReleaseNotesURL,
			InstallInstructionsMD: p.InstallInstructionsMD,
		})
	}

	licenses, err := st.ListLicenses(ctx)
	if err != nil {
		return nil, fmt.Errorf("list licenses: %w", err)
	}
	licRowIDToLicenseID := make(map[string]string, len(licenses))
	for _, l := range licenses {
		licRowIDToLicenseID[l.ID.String()] = l.LicenseID
		// We DO include the real licBlob — it's signed, contains no operator
		// secrets, and is required to recreate the row. If a deployment
		// wants the blob blacked out too they can post-process the export.
		mf.Spec.Licenses = append(mf.Spec.Licenses, apply.LicenseSpec{
			LicBlob: l.LicBlob,
		})
	}

	// Grants are emitted per-license. We look them up via the store using
	// the row UUID, then translate package IDs to slugs.
	for _, l := range licenses {
		rows, err := st.ListGrantsForLicense(ctx, l.ID)
		if err != nil {
			return nil, fmt.Errorf("list grants for %s: %w", l.LicenseID, err)
		}
		if len(rows) == 0 {
			continue
		}
		var pkgs []string
		var actions []string
		seenActions := make(map[string]struct{})
		for _, g := range rows {
			slug, ok := pkgIDToSlug[g.PackageID.String()]
			if !ok {
				continue // package was deleted under us; skip
			}
			pkgs = append(pkgs, slug)
			for _, a := range g.Actions {
				if _, dup := seenActions[a]; dup {
					continue
				}
				seenActions[a] = struct{}{}
				actions = append(actions, a)
			}
		}
		mf.Spec.Grants = append(mf.Spec.Grants, apply.GrantSpec{
			License:  l.LicenseID,
			Packages: pkgs,
			Actions:  actions,
		})
	}

	return mf, nil
}

func anyProviderTouched(rep *apply.ApplyReport) bool {
	for _, it := range rep.Items {
		if it.Kind == apply.KindOIDCProvider && it.Action != apply.ActionNoop {
			return true
		}
	}
	return false
}

func queryBool(r *http.Request, key string) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return v == "1" || v == "true" || v == "yes"
}
