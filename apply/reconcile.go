package apply

import (
	"context"
	"crypto/subtle"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
)

// Kind constants used in ApplyItem.Kind. Mirrors the resource_type strings
// used by the audit logger so dashboards stay consistent.
const (
	KindStaticAdmin        = "static-admin"
	KindOIDCProvider       = "oidc-provider"
	KindUpstreamCredential = "upstream-credential"
	KindPackage            = "package"
	KindLicense            = "license"
	KindGrant              = "grant"
)

// Action constants used in ApplyItem.Action.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionNoop   = "noop"
	ActionDelete = "delete"
)

// ApplyReport captures the outcome of a Reconcile call. JSON tags match the
// REST envelope returned by /api/v1/config/apply.
type ApplyReport struct {
	DryRun bool         `json:"dry_run"`
	Items  []ApplyItem  `json:"items"`
	Errors []ApplyError `json:"errors,omitempty"`
}

// ApplyItem is one row in the plan. Diff is a human-readable list of fields
// that changed for ActionUpdate (e.g. ["display_name","upstream_repo"]).
type ApplyItem struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Action string   `json:"action"`
	Diff   []string `json:"diff,omitempty"`
}

// ApplyError records a per-item failure. The reconciler attempts every item
// and accumulates errors rather than aborting on the first one — the operator
// gets a complete picture of what's wrong.
type ApplyError struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// Options configures Reconcile.
type Options struct {
	// DryRun computes the plan and reports diffs without writing anything.
	DryRun bool
	// Prune deletes manifest-managed resources that aren't in the manifest.
	// Currently only enforced for static_admins where source='manifest' rows
	// are unambiguously owned by this code path. Other kinds (packages,
	// licenses, etc.) are pruned conservatively — items in the manifest are
	// created/updated, items in the DB but NOT in the manifest are deleted.
	Prune bool
}

// Reconcile walks the manifest and brings the store into agreement with it.
//
// Order matters: upstreamCredentials → packages → licenses → grants →
// oidcProviders → staticAdmins. Each kind processes all of its items even if
// some fail (errors aggregate into the report), but cross-kind references
// (packages referencing credentials, grants referencing licenses+packages)
// require earlier kinds to have produced usable rows.
//
// The reconciler doesn't open its own transaction — pgxpool transactions are
// per-connection and we'd serialize concurrent applies in a way that hurts.
// Instead each item is best-effort: failures are recorded, prior writes
// remain. Operators retry by re-applying — operations are idempotent.
func Reconcile(
	ctx context.Context,
	st store.DataStore,
	crypto *auth.Crypto,
	verifier license.Verifier,
	mf *Manifest,
	opts Options,
) (*ApplyReport, error) {
	if mf == nil {
		return nil, fmt.Errorf("apply: nil manifest")
	}
	rep := &ApplyReport{DryRun: opts.DryRun}

	credNameToID, err := reconcileUpstreamCredentials(ctx, st, crypto, mf.Spec.UpstreamCredentials, opts, rep)
	if err != nil {
		return rep, err
	}
	pkgSlugToID, err := reconcilePackages(ctx, st, mf.Spec.Packages, credNameToID, opts, rep)
	if err != nil {
		return rep, err
	}
	licIDToRowID, err := reconcileLicenses(ctx, st, verifier, mf.Spec.Licenses, opts, rep)
	if err != nil {
		return rep, err
	}
	if err := reconcileGrants(ctx, st, mf.Spec.Grants, licIDToRowID, pkgSlugToID, opts, rep); err != nil {
		return rep, err
	}
	if err := reconcileOIDCProviders(ctx, st, crypto, mf.Spec.OIDCProviders, opts, rep); err != nil {
		return rep, err
	}
	if err := reconcileStaticAdmins(ctx, st, mf.Spec.StaticAdmins, opts, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

// --- upstream credentials ---------------------------------------------------

func reconcileUpstreamCredentials(
	ctx context.Context,
	st store.DataStore,
	crypto *auth.Crypto,
	specs []UpstreamCredentialSpec,
	opts Options,
	rep *ApplyReport,
) (map[string]uuid.UUID, error) {
	existing, err := st.ListUpstreamCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("list upstream credentials: %w", err)
	}
	byName := make(map[string]store.UpstreamCredential, len(existing))
	for _, c := range existing {
		byName[c.Name] = c
	}
	out := make(map[string]uuid.UUID, len(specs))
	seen := make(map[string]struct{}, len(specs))

	for _, s := range specs {
		if s.Name == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Message: "name is required"})
			continue
		}
		if _, dup := seen[s.Name]; dup {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: s.Name, Message: "duplicate name in manifest"})
			continue
		}
		seen[s.Name] = struct{}{}

		if s.PAT == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: s.Name, Message: "pat is required (set pat or patFromEnv)"})
			continue
		}
		kind := s.Kind
		if kind == "" {
			kind = "ghcr"
		}
		fp := crypto.Fingerprint([]byte(s.PAT))

		baseURL := strings.TrimRight(s.BaseURL, "/")

		if prev, ok := byName[s.Name]; ok {
			out[s.Name] = prev.ID
			var diff []string
			if prev.Kind != kind {
				diff = append(diff, "kind")
			}
			if prev.Username != s.Username {
				diff = append(diff, "username")
			}
			if prev.PATFingerprint != fp {
				diff = append(diff, "pat")
			}
			if prev.BaseURL != baseURL {
				diff = append(diff, "baseUrl")
			}
			if prev.CABundlePEM != s.CABundlePEM {
				diff = append(diff, "caBundlePem")
			}
			if prev.InsecureSkipTLSVerify != s.InsecureSkipTLSVerify {
				diff = append(diff, "insecureSkipTlsVerify")
			}
			if len(diff) == 0 {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindUpstreamCredential, Name: s.Name, Action: ActionNoop})
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindUpstreamCredential, Name: s.Name, Action: ActionUpdate, Diff: diff})
				continue
			}
			if err := st.DeleteUpstreamCredential(ctx, prev.ID); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: s.Name, Message: "delete-for-update: " + err.Error()})
				continue
			}
			sealed, err := crypto.Seal([]byte(s.PAT))
			if err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: s.Name, Message: "seal: " + err.Error()})
				continue
			}
			row := &store.UpstreamCredential{
				ID: prev.ID, Name: s.Name, Kind: kind, Username: s.Username,
				PATEnc: sealed, PATFingerprint: fp,
				BaseURL: baseURL, CABundlePEM: s.CABundlePEM, InsecureSkipTLSVerify: s.InsecureSkipTLSVerify,
			}
			if err := st.InsertUpstreamCredential(ctx, row); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: s.Name, Message: err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindUpstreamCredential, Name: s.Name, Action: ActionUpdate, Diff: diff})
			continue
		}

		if opts.DryRun {
			out[s.Name] = uuid.New()
			rep.Items = append(rep.Items, ApplyItem{Kind: KindUpstreamCredential, Name: s.Name, Action: ActionCreate})
			continue
		}
		sealed, err := crypto.Seal([]byte(s.PAT))
		if err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: s.Name, Message: "seal: " + err.Error()})
			continue
		}
		row := &store.UpstreamCredential{
			ID: uuid.New(), Name: s.Name, Kind: kind, Username: s.Username,
			PATEnc: sealed, PATFingerprint: fp,
			BaseURL: baseURL, CABundlePEM: s.CABundlePEM, InsecureSkipTLSVerify: s.InsecureSkipTLSVerify,
		}
		if err := st.InsertUpstreamCredential(ctx, row); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: s.Name, Message: err.Error()})
			continue
		}
		out[s.Name] = row.ID
		rep.Items = append(rep.Items, ApplyItem{Kind: KindUpstreamCredential, Name: s.Name, Action: ActionCreate})
	}

	if opts.Prune {
		for _, prev := range existing {
			if _, kept := seen[prev.Name]; kept {
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindUpstreamCredential, Name: prev.Name, Action: ActionDelete})
				continue
			}
			if err := st.DeleteUpstreamCredential(ctx, prev.ID); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: prev.Name, Message: "prune: " + err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindUpstreamCredential, Name: prev.Name, Action: ActionDelete})
		}
	}
	// Backfill `out` with rows we didn't touch but exist (lets packages
	// reference creds that weren't redeclared in the manifest).
	for _, prev := range existing {
		if _, has := out[prev.Name]; !has {
			out[prev.Name] = prev.ID
		}
	}
	return out, nil
}

// --- packages ---------------------------------------------------------------

func reconcilePackages(
	ctx context.Context,
	st store.DataStore,
	specs []PackageSpec,
	credNameToID map[string]uuid.UUID,
	opts Options,
	rep *ApplyReport,
) (map[string]uuid.UUID, error) {
	existing, err := st.ListPackages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	bySlug := make(map[string]store.Package, len(existing))
	for _, p := range existing {
		bySlug[p.Slug] = p
	}
	out := make(map[string]uuid.UUID, len(specs))
	seen := make(map[string]struct{}, len(specs))

	for _, s := range specs {
		if s.Slug == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindPackage, Message: "slug is required"})
			continue
		}
		if _, dup := seen[s.Slug]; dup {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindPackage, Name: s.Slug, Message: "duplicate slug in manifest"})
			continue
		}
		seen[s.Slug] = struct{}{}

		if s.UpstreamCredential == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindPackage, Name: s.Slug, Message: "upstreamCredential is required"})
			continue
		}
		credID, ok := credNameToID[s.UpstreamCredential]
		if !ok {
			rep.Errors = append(rep.Errors, ApplyError{
				Kind: KindPackage, Name: s.Slug,
				Message: fmt.Sprintf("upstreamCredential %q not found in manifest or DB", s.UpstreamCredential),
			})
			continue
		}
		source := s.Source
		if source == "" {
			source = "oci"
		}
		switch source {
		case "oci":
			if s.Path == "" || s.UpstreamRepo == "" {
				rep.Errors = append(rep.Errors, ApplyError{
					Kind: KindPackage, Name: s.Slug,
					Message: "source=oci requires path and upstreamRepo",
				})
				continue
			}
		case "github-release":
			if s.GitHubRepo == "" {
				rep.Errors = append(rep.Errors, ApplyError{
					Kind: KindPackage, Name: s.Slug,
					Message: "source=github-release requires githubRepo",
				})
				continue
			}
		case "gitlab-release":
			// The Package.GitHubRepo column is reused as the upstream
			// project path for gitlab-release; renaming the column would
			// be churn for no behavioral gain. The manifest still uses
			// `githubRepo` for both — confusing but stable.
			if s.GitHubRepo == "" {
				rep.Errors = append(rep.Errors, ApplyError{
					Kind: KindPackage, Name: s.Slug,
					Message: "source=gitlab-release requires githubRepo (used as the GitLab project path, e.g. group/subgroup/project)",
				})
				continue
			}
		default:
			rep.Errors = append(rep.Errors, ApplyError{
				Kind: KindPackage, Name: s.Slug,
				Message: "source must be 'oci', 'github-release', or 'gitlab-release'",
			})
			continue
		}
		if s.Kind == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindPackage, Name: s.Slug, Message: "kind is required"})
			continue
		}

		desired := store.Package{
			Slug: s.Slug, Path: s.Path, UpstreamRepo: s.UpstreamRepo,
			UpstreamCredentialID: credID, Kind: s.Kind,
			DisplayName: s.DisplayName, Description: s.Description,
			ReleaseNotesURL: s.ReleaseNotesURL, InstallInstructionsMD: s.InstallInstructionsMD,
			Source: source, GitHubRepo: s.GitHubRepo,
			ReleasePattern: s.ReleasePattern, AssetPattern: s.AssetPattern,
		}

		if prev, ok := bySlug[s.Slug]; ok {
			out[s.Slug] = prev.ID
			diff := diffPackage(&prev, &desired)
			if len(diff) == 0 {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindPackage, Name: s.Slug, Action: ActionNoop})
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindPackage, Name: s.Slug, Action: ActionUpdate, Diff: diff})
				continue
			}
			desired.ID = prev.ID
			desired.CreatedAt = prev.CreatedAt
			if err := st.UpdatePackage(ctx, &desired); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindPackage, Name: s.Slug, Message: err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindPackage, Name: s.Slug, Action: ActionUpdate, Diff: diff})
			continue
		}

		// Create.
		if opts.DryRun {
			out[s.Slug] = uuid.New()
			rep.Items = append(rep.Items, ApplyItem{Kind: KindPackage, Name: s.Slug, Action: ActionCreate})
			continue
		}
		desired.ID = uuid.New()
		if err := st.InsertPackage(ctx, &desired); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindPackage, Name: s.Slug, Message: err.Error()})
			continue
		}
		out[s.Slug] = desired.ID
		rep.Items = append(rep.Items, ApplyItem{Kind: KindPackage, Name: s.Slug, Action: ActionCreate})
	}

	if opts.Prune {
		for _, prev := range existing {
			if _, kept := seen[prev.Slug]; kept {
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindPackage, Name: prev.Slug, Action: ActionDelete})
				continue
			}
			if err := st.DeletePackage(ctx, prev.ID); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindPackage, Name: prev.Slug, Message: "prune: " + err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindPackage, Name: prev.Slug, Action: ActionDelete})
		}
	}
	for _, prev := range existing {
		if _, has := out[prev.Slug]; !has {
			out[prev.Slug] = prev.ID
		}
	}
	return out, nil
}

func diffPackage(prev, next *store.Package) []string {
	var d []string
	if prev.Path != next.Path {
		d = append(d, "path")
	}
	if prev.UpstreamRepo != next.UpstreamRepo {
		d = append(d, "upstream_repo")
	}
	if prev.UpstreamCredentialID != next.UpstreamCredentialID {
		d = append(d, "upstream_credential_id")
	}
	if prev.Kind != next.Kind {
		d = append(d, "kind")
	}
	if prev.DisplayName != next.DisplayName {
		d = append(d, "display_name")
	}
	if prev.Description != next.Description {
		d = append(d, "description")
	}
	if prev.ReleaseNotesURL != next.ReleaseNotesURL {
		d = append(d, "release_notes_url")
	}
	if prev.InstallInstructionsMD != next.InstallInstructionsMD {
		d = append(d, "install_instructions_md")
	}
	if prev.Source != next.Source {
		d = append(d, "source")
	}
	if prev.GitHubRepo != next.GitHubRepo {
		d = append(d, "github_repo")
	}
	if prev.ReleasePattern != next.ReleasePattern {
		d = append(d, "release_pattern")
	}
	if prev.AssetPattern != next.AssetPattern {
		d = append(d, "asset_pattern")
	}
	return d
}

// --- licenses ---------------------------------------------------------------

func reconcileLicenses(
	ctx context.Context,
	st store.DataStore,
	verifier license.Verifier,
	specs []LicenseSpec,
	opts Options,
	rep *ApplyReport,
) (map[string]uuid.UUID, error) {
	existing, err := st.ListLicenses(ctx)
	if err != nil {
		return nil, fmt.Errorf("list licenses: %w", err)
	}
	byLicenseID := make(map[string]store.License, len(existing))
	for _, l := range existing {
		byLicenseID[l.LicenseID] = l
	}
	out := make(map[string]uuid.UUID, len(specs))
	seen := make(map[string]struct{}, len(specs))

	for _, s := range specs {
		if strings.TrimSpace(s.LicBlob) == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindLicense, Message: "licBlob is required"})
			continue
		}
		parsed, err := verifier.VerifyLicenseBlob(s.LicBlob)
		if err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindLicense, Message: "invalid license: " + err.Error()})
			continue
		}
		if parsed.ID == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindLicense, Message: "parsed license has no id"})
			continue
		}
		if _, dup := seen[parsed.ID]; dup {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindLicense, Name: parsed.ID, Message: "duplicate license id in manifest"})
			continue
		}
		seen[parsed.ID] = struct{}{}

		if prev, ok := byLicenseID[parsed.ID]; ok {
			out[parsed.ID] = prev.ID
			// Licenses are immutable apart from revocation; if the blob is
			// identical it's a noop, otherwise we surface that the blob
			// changed (we don't rewrite — re-importing a license is rare and
			// safer handled deliberately by the admin).
			if prev.LicBlob == s.LicBlob {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindLicense, Name: parsed.ID, Action: ActionNoop})
			} else {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindLicense, Name: parsed.ID, Action: ActionNoop, Diff: []string{"lic_blob (changed; reimport not auto-applied)"}})
			}
			continue
		}

		if opts.DryRun {
			out[parsed.ID] = uuid.New()
			rep.Items = append(rep.Items, ApplyItem{Kind: KindLicense, Name: parsed.ID, Action: ActionCreate})
			continue
		}
		row := &store.License{
			ID:           uuid.New(),
			LicenseID:    parsed.ID,
			Customer:     parsed.Customer,
			Organization: parsed.Organization,
			Tier:         parsed.Tier,
			LicBlob:      s.LicBlob,
		}
		if exp, ok := parseLicenseExpiry(parsed); ok {
			row.ExpiresAt = &exp
		}
		if err := st.InsertLicense(ctx, row); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindLicense, Name: parsed.ID, Message: err.Error()})
			continue
		}
		out[parsed.ID] = row.ID
		rep.Items = append(rep.Items, ApplyItem{Kind: KindLicense, Name: parsed.ID, Action: ActionCreate})
	}

	if opts.Prune {
		for _, prev := range existing {
			if _, kept := seen[prev.LicenseID]; kept {
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindLicense, Name: prev.LicenseID, Action: ActionDelete})
				continue
			}
			if err := st.DeleteLicense(ctx, prev.ID); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindLicense, Name: prev.LicenseID, Message: "prune: " + err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindLicense, Name: prev.LicenseID, Action: ActionDelete})
		}
	}
	for _, prev := range existing {
		if _, has := out[prev.LicenseID]; !has {
			out[prev.LicenseID] = prev.ID
		}
	}
	return out, nil
}

// parseLicenseExpiry duplicates server/admin.go's helper to keep the apply
// package free of cross-package coupling with server/.
func parseLicenseExpiry(l *license.License) (time.Time, bool) {
	if l.ExpiresAt == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, l.ExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// --- grants ----------------------------------------------------------------

func reconcileGrants(
	ctx context.Context,
	st store.DataStore,
	specs []GrantSpec,
	licIDToRowID map[string]uuid.UUID,
	pkgSlugToID map[string]uuid.UUID,
	opts Options,
	rep *ApplyReport,
) error {
	seen := make(map[string]struct{}, len(specs))
	for _, g := range specs {
		if g.License == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindGrant, Message: "license is required"})
			continue
		}
		if _, dup := seen[g.License]; dup {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindGrant, Name: g.License, Message: "duplicate license in grants"})
			continue
		}
		seen[g.License] = struct{}{}

		licRowID, ok := licIDToRowID[g.License]
		if !ok {
			rep.Errors = append(rep.Errors, ApplyError{
				Kind: KindGrant, Name: g.License,
				Message: fmt.Sprintf("license %q not found in manifest or DB", g.License),
			})
			continue
		}
		actions := g.Actions
		if len(actions) == 0 {
			actions = []string{"pull"}
		}
		// Resolve package slugs to UUIDs; first missing slug fails the grant.
		var pkgIDs []uuid.UUID
		var missing []string
		for _, slug := range g.Packages {
			id, ok := pkgSlugToID[slug]
			if !ok {
				missing = append(missing, slug)
				continue
			}
			pkgIDs = append(pkgIDs, id)
		}
		if len(missing) > 0 {
			rep.Errors = append(rep.Errors, ApplyError{
				Kind: KindGrant, Name: g.License,
				Message: "unknown package slugs: " + strings.Join(missing, ","),
			})
			continue
		}

		// Compare to current state to decide create/update/noop.
		current, err := st.ListGrantsForLicense(ctx, licRowID)
		if err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindGrant, Name: g.License, Message: err.Error()})
			continue
		}
		if grantsEqual(current, pkgIDs, actions) {
			rep.Items = append(rep.Items, ApplyItem{Kind: KindGrant, Name: g.License, Action: ActionNoop})
			continue
		}
		action := ActionUpdate
		if len(current) == 0 {
			action = ActionCreate
		}
		if opts.DryRun {
			rep.Items = append(rep.Items, ApplyItem{Kind: KindGrant, Name: g.License, Action: action})
			continue
		}
		if err := st.ReplaceGrantsForLicense(ctx, licRowID, pkgIDs, actions); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindGrant, Name: g.License, Message: err.Error()})
			continue
		}
		rep.Items = append(rep.Items, ApplyItem{Kind: KindGrant, Name: g.License, Action: action})
	}
	return nil
}

// grantsEqual checks set equality of (package_id, actions) against desired.
// We treat actions as identical when their sorted contents match.
func grantsEqual(current []store.PackageGrant, wantPkgIDs []uuid.UUID, wantActions []string) bool {
	if len(current) != len(wantPkgIDs) {
		return false
	}
	wantSet := make(map[uuid.UUID]struct{}, len(wantPkgIDs))
	for _, id := range wantPkgIDs {
		wantSet[id] = struct{}{}
	}
	wantA := append([]string(nil), wantActions...)
	sort.Strings(wantA)
	for _, g := range current {
		if _, ok := wantSet[g.PackageID]; !ok {
			return false
		}
		gotA := append([]string(nil), g.Actions...)
		sort.Strings(gotA)
		if !stringSliceEqual(gotA, wantA) {
			return false
		}
	}
	return true
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- oidc providers ---------------------------------------------------------

func reconcileOIDCProviders(
	ctx context.Context,
	st store.DataStore,
	crypto *auth.Crypto,
	specs []OIDCProviderSpec,
	opts Options,
	rep *ApplyReport,
) error {
	existing, err := st.ListOIDCProviders(ctx)
	if err != nil {
		return fmt.Errorf("list oidc providers: %w", err)
	}
	byName := make(map[string]store.OIDCProvider, len(existing))
	for _, p := range existing {
		byName[p.Name] = p
	}
	seen := make(map[string]struct{}, len(specs))

	for _, s := range specs {
		if s.Name == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindOIDCProvider, Message: "name is required"})
			continue
		}
		if _, dup := seen[s.Name]; dup {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindOIDCProvider, Name: s.Name, Message: "duplicate name in manifest"})
			continue
		}
		seen[s.Name] = struct{}{}

		if s.IssuerURL == "" || s.ClientID == "" || s.ClientSecret == "" {
			rep.Errors = append(rep.Errors, ApplyError{
				Kind: KindOIDCProvider, Name: s.Name,
				Message: "issuerUrl, clientId, and clientSecret are required",
			})
			continue
		}

		if prev, ok := byName[s.Name]; ok {
			// Diff via best-effort decrypt; if decrypt fails we treat secret
			// as different and re-seal.
			var diff []string
			if prev.IssuerURL != s.IssuerURL {
				diff = append(diff, "issuer_url")
			}
			if prev.ClientID != s.ClientID {
				diff = append(diff, "client_id")
			}
			if prev.Enabled != s.Enabled {
				diff = append(diff, "enabled")
			}
			if !scopesEqual(prev.Scopes, s.Scopes) {
				diff = append(diff, "scopes")
			}
			secretChanged := true
			if cur, err := crypto.Open(prev.ClientSecretEnc); err == nil {
				secretChanged = subtle.ConstantTimeCompare(cur, []byte(s.ClientSecret)) != 1
			}
			if secretChanged {
				diff = append(diff, "client_secret")
			}
			if len(diff) == 0 {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindOIDCProvider, Name: s.Name, Action: ActionNoop})
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindOIDCProvider, Name: s.Name, Action: ActionUpdate, Diff: diff})
				continue
			}
			// No Update on the store; delete + reinsert preserving ID.
			if err := st.DeleteOIDCProvider(ctx, prev.ID); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindOIDCProvider, Name: s.Name, Message: "delete-for-update: " + err.Error()})
				continue
			}
			sealed, err := crypto.Seal([]byte(s.ClientSecret))
			if err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindOIDCProvider, Name: s.Name, Message: "seal: " + err.Error()})
				continue
			}
			row := &store.OIDCProvider{
				ID: prev.ID, Name: s.Name, IssuerURL: s.IssuerURL,
				ClientID: s.ClientID, ClientSecretEnc: sealed,
				Scopes: s.Scopes, Enabled: s.Enabled,
			}
			if err := st.InsertOIDCProvider(ctx, row); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindOIDCProvider, Name: s.Name, Message: err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindOIDCProvider, Name: s.Name, Action: ActionUpdate, Diff: diff})
			continue
		}

		if opts.DryRun {
			rep.Items = append(rep.Items, ApplyItem{Kind: KindOIDCProvider, Name: s.Name, Action: ActionCreate})
			continue
		}
		sealed, err := crypto.Seal([]byte(s.ClientSecret))
		if err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindOIDCProvider, Name: s.Name, Message: "seal: " + err.Error()})
			continue
		}
		row := &store.OIDCProvider{
			ID: uuid.New(), Name: s.Name, IssuerURL: s.IssuerURL,
			ClientID: s.ClientID, ClientSecretEnc: sealed,
			Scopes: s.Scopes, Enabled: s.Enabled,
		}
		if err := st.InsertOIDCProvider(ctx, row); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindOIDCProvider, Name: s.Name, Message: err.Error()})
			continue
		}
		rep.Items = append(rep.Items, ApplyItem{Kind: KindOIDCProvider, Name: s.Name, Action: ActionCreate})
	}

	if opts.Prune {
		for _, prev := range existing {
			if _, kept := seen[prev.Name]; kept {
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindOIDCProvider, Name: prev.Name, Action: ActionDelete})
				continue
			}
			if err := st.DeleteOIDCProvider(ctx, prev.ID); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindOIDCProvider, Name: prev.Name, Message: "prune: " + err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindOIDCProvider, Name: prev.Name, Action: ActionDelete})
		}
	}
	return nil
}

func scopesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return stringSliceEqual(aa, bb)
}

// --- static admins ---------------------------------------------------------

// staticAdminSource marks rows owned by the manifest layer; only rows with
// this source value are touched by reconcile/prune. Operators manually
// inserted rows with a different source (or env-var entries from
// cfg.StaticAdmins, which never hit this table) stay untouched.
const staticAdminSource = "manifest"

func reconcileStaticAdmins(
	ctx context.Context,
	st store.DataStore,
	specs []StaticAdminSpec,
	opts Options,
	rep *ApplyReport,
) error {
	existing, err := st.ListStaticAdmins(ctx)
	if err != nil {
		return fmt.Errorf("list static admins: %w", err)
	}
	byEmail := make(map[string]store.StaticAdmin, len(existing))
	for _, sa := range existing {
		byEmail[strings.ToLower(sa.Email)] = sa
	}
	seen := make(map[string]struct{}, len(specs))

	for _, s := range specs {
		email := strings.ToLower(strings.TrimSpace(s.Email))
		if email == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindStaticAdmin, Message: "email is required"})
			continue
		}
		if _, dup := seen[email]; dup {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindStaticAdmin, Name: email, Message: "duplicate email in manifest"})
			continue
		}
		seen[email] = struct{}{}
		if s.Password == "" {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindStaticAdmin, Name: email, Message: "password is required (set password or passwordFromEnv)"})
			continue
		}

		prev, exists := byEmail[email]
		if exists {
			// Compare by VerifyPassword — same plaintext yields a noop, new
			// plaintext gets re-hashed.
			if auth.VerifyPassword(prev.PasswordHash, s.Password) == nil {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindStaticAdmin, Name: email, Action: ActionNoop})
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindStaticAdmin, Name: email, Action: ActionUpdate, Diff: []string{"password"}})
				continue
			}
			hash, err := auth.HashPassword(s.Password)
			if err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindStaticAdmin, Name: email, Message: "hash: " + err.Error()})
				continue
			}
			updated := &store.StaticAdmin{
				ID: prev.ID, Email: email, PasswordHash: hash, Source: staticAdminSource,
			}
			if err := st.UpsertStaticAdmin(ctx, updated); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindStaticAdmin, Name: email, Message: err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindStaticAdmin, Name: email, Action: ActionUpdate, Diff: []string{"password"}})
			continue
		}
		if opts.DryRun {
			rep.Items = append(rep.Items, ApplyItem{Kind: KindStaticAdmin, Name: email, Action: ActionCreate})
			continue
		}
		hash, err := auth.HashPassword(s.Password)
		if err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindStaticAdmin, Name: email, Message: "hash: " + err.Error()})
			continue
		}
		row := &store.StaticAdmin{
			ID: uuid.New(), Email: email, PasswordHash: hash, Source: staticAdminSource,
		}
		if err := st.UpsertStaticAdmin(ctx, row); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindStaticAdmin, Name: email, Message: err.Error()})
			continue
		}
		rep.Items = append(rep.Items, ApplyItem{Kind: KindStaticAdmin, Name: email, Action: ActionCreate})
	}

	if opts.Prune {
		for _, prev := range existing {
			if prev.Source != staticAdminSource {
				continue
			}
			if _, kept := seen[strings.ToLower(prev.Email)]; kept {
				continue
			}
			if opts.DryRun {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindStaticAdmin, Name: prev.Email, Action: ActionDelete})
				continue
			}
			if err := st.DeleteStaticAdmin(ctx, prev.ID); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{Kind: KindStaticAdmin, Name: prev.Email, Message: "prune: " + err.Error()})
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindStaticAdmin, Name: prev.Email, Action: ActionDelete})
		}
	}
	return nil
}
