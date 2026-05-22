package apply

import (
	"context"
	"fmt"
	"net/mail"
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
	KindUpstreamCredential = "upstream-credential"
	KindPackage            = "package"
	KindLicense            = "license"
	KindGrant              = "grant"
	KindContact            = "contact"
)

// Action constants used in ApplyItem.Action.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionNoop   = "noop"
	ActionDelete = "delete"
)

// sourceManifest tags rows owned by the manifest reconciler. Prune only
// touches rows whose `source` column equals this value — admin-UI-created
// rows (which write source='') are left alone even when they aren't named in
// the manifest.
const sourceManifest = "manifest"

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
	// Only rows tagged source='manifest' are touched — admin-UI-created rows
	// (source='') are preserved even when they don't appear in the manifest.
	// Prune passes run dependents-first (grants -> packages -> licenses ->
	// credentials) so FK RESTRICT constraints can't trip the deletion order.
	Prune bool
}

// Reconcile walks the manifest and brings the store into agreement with it.
//
// Order matters in two distinct phases:
//
//   create/update: upstreamCredentials -> packages -> licenses -> grants
//   prune        : grants -> packages -> licenses -> credentials
//
// The prune phase runs dependents-first so the FK from packages to
// upstream_credentials (ON DELETE RESTRICT) cannot block a credential delete.
//
// Each item is best-effort: failures are recorded in rep.Errors, prior writes
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

	credState, err := reconcileUpstreamCredentials(ctx, st, crypto, mf.Spec.UpstreamCredentials, opts, rep)
	if err != nil {
		return rep, err
	}
	pkgState, err := reconcilePackages(ctx, st, mf.Spec.Packages, credState.nameToID, opts, rep)
	if err != nil {
		return rep, err
	}
	licState, err := reconcileLicenses(ctx, st, verifier, mf.Spec.Licenses, opts, rep)
	if err != nil {
		return rep, err
	}
	contactsTouched := reconcileContacts(ctx, st, verifier, mf.Spec.Licenses, licState.idToRowID, opts, rep)
	grantsTouched, err := reconcileGrants(ctx, st, mf.Spec.Grants, licState.idToRowID, pkgState.slugToID, opts, rep)
	if err != nil {
		return rep, err
	}

	// Prune phase: dependents-first so FK RESTRICT cannot block deletes.
	if opts.Prune {
		pruneGrants(ctx, st, licState.existing, grantsTouched, opts, rep)
		pruneContacts(ctx, st, licState.existing, contactsTouched, opts, rep)
		prunePackages(ctx, st, pkgState.existing, pkgState.seen, opts, rep)
		pruneLicenses(ctx, st, licState.existing, licState.seen, opts, rep)
		pruneUpstreamCredentials(ctx, st, credState.existing, credState.seen, opts, rep)
	}

	return rep, nil
}

// --- upstream credentials ---------------------------------------------------

type upstreamCredentialState struct {
	nameToID map[string]uuid.UUID
	existing []store.UpstreamCredential
	seen     map[string]struct{}
}

func reconcileUpstreamCredentials(
	ctx context.Context,
	st store.DataStore,
	crypto *auth.Crypto,
	specs []UpstreamCredentialSpec,
	opts Options,
	rep *ApplyReport,
) (*upstreamCredentialState, error) {
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
			// Tagging a previously-untagged row counts as a change so we can
			// surface that the manifest is "adopting" it on the next apply.
			if prev.Source != sourceManifest {
				diff = append(diff, "source")
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
				Source: sourceManifest,
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
			Source: sourceManifest,
		}
		if err := st.InsertUpstreamCredential(ctx, row); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindUpstreamCredential, Name: s.Name, Message: err.Error()})
			continue
		}
		out[s.Name] = row.ID
		rep.Items = append(rep.Items, ApplyItem{Kind: KindUpstreamCredential, Name: s.Name, Action: ActionCreate})
	}

	// Backfill `out` with rows we didn't touch but exist (lets packages
	// reference creds that weren't redeclared in the manifest).
	for _, prev := range existing {
		if _, has := out[prev.Name]; !has {
			out[prev.Name] = prev.ID
		}
	}
	return &upstreamCredentialState{nameToID: out, existing: existing, seen: seen}, nil
}

func pruneUpstreamCredentials(
	ctx context.Context,
	st store.DataStore,
	existing []store.UpstreamCredential,
	seen map[string]struct{},
	opts Options,
	rep *ApplyReport,
) {
	for _, prev := range existing {
		if _, kept := seen[prev.Name]; kept {
			continue
		}
		// Only prune rows the manifest owns. Admin-UI-created rows (source='')
		// are not part of any manifest's world.
		if prev.Source != sourceManifest {
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

// --- packages ---------------------------------------------------------------

type packageState struct {
	slugToID map[string]uuid.UUID
	existing []store.Package
	seen     map[string]struct{}
}

func reconcilePackages(
	ctx context.Context,
	st store.DataStore,
	specs []PackageSpec,
	credNameToID map[string]uuid.UUID,
	opts Options,
	rep *ApplyReport,
) (*packageState, error) {
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
			ManagedBy: sourceManifest,
		}

		if prev, ok := bySlug[s.Slug]; ok {
			out[s.Slug] = prev.ID
			diff := diffPackage(&prev, &desired)
			if prev.ManagedBy != sourceManifest {
				diff = append(diff, "managed_by")
			}
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

	for _, prev := range existing {
		if _, has := out[prev.Slug]; !has {
			out[prev.Slug] = prev.ID
		}
	}
	return &packageState{slugToID: out, existing: existing, seen: seen}, nil
}

func prunePackages(
	ctx context.Context,
	st store.DataStore,
	existing []store.Package,
	seen map[string]struct{},
	opts Options,
	rep *ApplyReport,
) {
	for _, prev := range existing {
		if _, kept := seen[prev.Slug]; kept {
			continue
		}
		if prev.ManagedBy != sourceManifest {
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

type licenseState struct {
	idToRowID map[string]uuid.UUID
	existing  []store.License
	seen      map[string]struct{}
}

func reconcileLicenses(
	ctx context.Context,
	st store.DataStore,
	verifier license.Verifier,
	specs []LicenseSpec,
	opts Options,
	rep *ApplyReport,
) (*licenseState, error) {
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
			Source:       sourceManifest,
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

	for _, prev := range existing {
		if _, has := out[prev.LicenseID]; !has {
			out[prev.LicenseID] = prev.ID
		}
	}
	return &licenseState{idToRowID: out, existing: existing, seen: seen}, nil
}

func pruneLicenses(
	ctx context.Context,
	st store.DataStore,
	existing []store.License,
	seen map[string]struct{},
	opts Options,
	rep *ApplyReport,
) {
	for _, prev := range existing {
		if _, kept := seen[prev.LicenseID]; kept {
			continue
		}
		if prev.Source != sourceManifest {
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

// reconcileGrants applies the manifest's grant set. Returns the set of
// license row UUIDs that the manifest "touched" — used by pruneGrants to
// distinguish "license still in manifest but no grants here" (clear orphans)
// from "license absent from manifest entirely" (cascades when the license
// row is dropped).
func reconcileGrants(
	ctx context.Context,
	st store.DataStore,
	specs []GrantSpec,
	licIDToRowID map[string]uuid.UUID,
	pkgSlugToID map[string]uuid.UUID,
	opts Options,
	rep *ApplyReport,
) (map[uuid.UUID]struct{}, error) {
	seen := make(map[string]struct{}, len(specs))
	touchedLicRowIDs := make(map[uuid.UUID]struct{}, len(specs))
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
		touchedLicRowIDs[licRowID] = struct{}{}
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
	return touchedLicRowIDs, nil
}

// pruneGrants handles the "license kept, grants emptied" case: when a license
// is still present in the manifest but its grants entry was zeroed/dropped,
// we clear the existing grants. The reverse case ("license dropped entirely")
// already cascades through licenses.id ON DELETE CASCADE — no work needed.
func pruneGrants(
	ctx context.Context,
	st store.DataStore,
	existingLicenses []store.License,
	touched map[uuid.UUID]struct{},
	opts Options,
	rep *ApplyReport,
) {
	for _, lic := range existingLicenses {
		if _, t := touched[lic.ID]; t {
			continue
		}
		// Only consider licenses managed by the manifest; admin-UI licenses
		// own their own grant set.
		if lic.Source != sourceManifest {
			continue
		}
		current, err := st.ListGrantsForLicense(ctx, lic.ID)
		if err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindGrant, Name: lic.LicenseID, Message: "list-for-prune: " + err.Error()})
			continue
		}
		if len(current) == 0 {
			continue
		}
		if opts.DryRun {
			rep.Items = append(rep.Items, ApplyItem{Kind: KindGrant, Name: lic.LicenseID, Action: ActionDelete})
			continue
		}
		if err := st.ReplaceGrantsForLicense(ctx, lic.ID, nil, nil); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindGrant, Name: lic.LicenseID, Message: "prune: " + err.Error()})
			continue
		}
		rep.Items = append(rep.Items, ApplyItem{Kind: KindGrant, Name: lic.LicenseID, Action: ActionDelete})
	}
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

// --- contacts ---------------------------------------------------------------

// reconcileContacts applies each license spec's contacts list. Returns the set
// of license row UUIDs whose contact set the manifest touched — used by
// pruneContacts to distinguish "license still in manifest, no contacts here"
// (clear manifest-owned rows) from "license absent from manifest entirely"
// (which we leave alone; the license itself will be pruned or kept based on
// its own source tag, and CASCADE will tidy up if it goes).
func reconcileContacts(
	ctx context.Context,
	st store.DataStore,
	verifier license.Verifier,
	licSpecs []LicenseSpec,
	licIDToRowID map[string]uuid.UUID,
	opts Options,
	rep *ApplyReport,
) map[uuid.UUID]struct{} {
	touched := make(map[uuid.UUID]struct{}, len(licSpecs))
	for _, ls := range licSpecs {
		// Re-verify the blob to recover the canonical license ID. If the
		// license itself failed to reconcile (idToRowID has no entry) we skip
		// — the error was already recorded by reconcileLicenses.
		parsed, err := verifier.VerifyLicenseBlob(ls.LicBlob)
		if err != nil || parsed == nil || parsed.ID == "" {
			continue
		}
		licIDGuess := parsed.ID
		licRowID, ok := licIDToRowID[licIDGuess]
		if !ok {
			continue
		}
		touched[licRowID] = struct{}{}

		// Validate, lowercase, trim, dedupe within this license spec.
		desired := make([]store.LicenseContact, 0, len(ls.Contacts))
		seenEmail := make(map[string]struct{}, len(ls.Contacts))
		for _, c := range ls.Contacts {
			email := strings.ToLower(strings.TrimSpace(c.Email))
			if email == "" {
				rep.Errors = append(rep.Errors, ApplyError{
					Kind: KindContact, Name: licIDGuess, Message: "email is required",
				})
				continue
			}
			if _, err := mail.ParseAddress(email); err != nil {
				rep.Errors = append(rep.Errors, ApplyError{
					Kind: KindContact, Name: licIDGuess + "/" + email,
					Message: "invalid email: " + err.Error(),
				})
				continue
			}
			if _, dup := seenEmail[email]; dup {
				rep.Errors = append(rep.Errors, ApplyError{
					Kind: KindContact, Name: licIDGuess + "/" + email,
					Message: "duplicate email in manifest entry",
				})
				continue
			}
			seenEmail[email] = struct{}{}
			desired = append(desired, store.LicenseContact{
				LicenseID: licRowID,
				Email:     email,
				Name:      strings.TrimSpace(c.Name),
				Source:    sourceManifest,
			})
		}

		current, err := st.ListManifestContactsForLicense(ctx, licRowID)
		if err != nil {
			rep.Errors = append(rep.Errors, ApplyError{
				Kind: KindContact, Name: licIDGuess, Message: err.Error(),
			})
			continue
		}
		currentByEmail := make(map[string]store.LicenseContact, len(current))
		for _, c := range current {
			currentByEmail[strings.ToLower(c.Email)] = c
		}
		desiredByEmail := make(map[string]store.LicenseContact, len(desired))
		for _, c := range desired {
			desiredByEmail[c.Email] = c
		}

		// Emit per-email plan items so the report is granular.
		for _, want := range desired {
			itemName := licIDGuess + "/" + want.Email
			if prev, has := currentByEmail[want.Email]; has {
				if prev.Name == want.Name {
					rep.Items = append(rep.Items, ApplyItem{Kind: KindContact, Name: itemName, Action: ActionNoop})
				} else {
					rep.Items = append(rep.Items, ApplyItem{Kind: KindContact, Name: itemName, Action: ActionUpdate, Diff: []string{"name"}})
				}
			} else {
				rep.Items = append(rep.Items, ApplyItem{Kind: KindContact, Name: itemName, Action: ActionCreate})
			}
		}
		for email, prev := range currentByEmail {
			if _, kept := desiredByEmail[email]; kept {
				continue
			}
			rep.Items = append(rep.Items, ApplyItem{Kind: KindContact, Name: licIDGuess + "/" + prev.Email, Action: ActionDelete})
		}

		if opts.DryRun {
			continue
		}
		if err := st.ReplaceManifestContactsForLicense(ctx, licRowID, desired); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{
				Kind: KindContact, Name: licIDGuess, Message: err.Error(),
			})
		}
	}
	return touched
}

// pruneContacts clears manifest-owned contacts for licenses that exist in the
// DB as manifest-managed but were absent from this apply. UI contacts on the
// same license (source='') are preserved.
func pruneContacts(
	ctx context.Context,
	st store.DataStore,
	existingLicenses []store.License,
	touched map[uuid.UUID]struct{},
	opts Options,
	rep *ApplyReport,
) {
	for _, lic := range existingLicenses {
		if _, t := touched[lic.ID]; t {
			continue
		}
		if lic.Source != sourceManifest {
			continue
		}
		current, err := st.ListManifestContactsForLicense(ctx, lic.ID)
		if err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindContact, Name: lic.LicenseID, Message: "list-for-prune: " + err.Error()})
			continue
		}
		if len(current) == 0 {
			continue
		}
		for _, c := range current {
			rep.Items = append(rep.Items, ApplyItem{Kind: KindContact, Name: lic.LicenseID + "/" + c.Email, Action: ActionDelete})
		}
		if opts.DryRun {
			continue
		}
		if err := st.ReplaceManifestContactsForLicense(ctx, lic.ID, nil); err != nil {
			rep.Errors = append(rep.Errors, ApplyError{Kind: KindContact, Name: lic.LicenseID, Message: "prune: " + err.Error()})
		}
	}
}
