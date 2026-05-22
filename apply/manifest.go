// Package apply implements the declarative ArtifactGatewayConfig custom
// resource: parsing, env-resolution, and reconciliation of an entire gateway
// configuration document against the database.
//
// The CR shape mirrors a Kubernetes-style resource so admins can apply the
// same YAML through the UI, via `kubectl create -f`-style tooling, or mount
// it at startup via the CONFIG_FILE env var.
//
// Auth is intentionally NOT part of the CR — OIDC providers and the bootstrap
// admin are configured at deploy-time via env vars (DEX_*, ADMIN_BOOTSTRAP_*)
// so the CR stays focused on data-plane configuration (credentials, packages,
// licenses, grants) that operators legitimately edit at runtime.
package apply

// APIVersion is the only apiVersion this package accepts.
const APIVersion = "artifact-gateway.cnak.us/v1"

// Kind is the only kind this package accepts.
const Kind = "ArtifactGatewayConfig"

// Manifest is the top-level document.
type Manifest struct {
	APIVersion string       `yaml:"apiVersion" json:"apiVersion"`
	Kind       string       `yaml:"kind"       json:"kind"`
	Metadata   Metadata     `yaml:"metadata"   json:"metadata"`
	Spec       ManifestSpec `yaml:"spec"       json:"spec"`
}

// Metadata is informational only — `name` is not used for resolution.
type Metadata struct {
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}

// ManifestSpec holds every kind of resource the gateway reconciles.
//
// Auth (oidcProviders, staticAdmins) is deliberately omitted — see package
// doc comment.
type ManifestSpec struct {
	UpstreamCredentials []UpstreamCredentialSpec `yaml:"upstreamCredentials,omitempty" json:"upstreamCredentials,omitempty"`
	Packages            []PackageSpec            `yaml:"packages,omitempty"            json:"packages,omitempty"`
	Licenses            []LicenseSpec            `yaml:"licenses,omitempty"            json:"licenses,omitempty"`
	Grants              []GrantSpec              `yaml:"grants,omitempty"              json:"grants,omitempty"`
}

// UpstreamCredentialSpec is a stored PAT used to pull from an upstream
// registry. The Kind discriminator selects how the proxy authenticates:
//   - ghcr        Basic auth against ghcr.io with `read:packages`.
//   - github-api  GitHub Releases REST API (asset downloads).
//   - oci-basic   Any Basic-auth OCI registry (Gitea, Harbor, Artifactory,
//                 ACR scope-mapped tokens). Requires BaseURL.
type UpstreamCredentialSpec struct {
	Name                  string `yaml:"name"                              json:"name"`
	Kind                  string `yaml:"kind"                              json:"kind"` // ghcr | github-api | oci-basic
	Username              string `yaml:"username"                          json:"username"`
	PAT                   string `yaml:"pat,omitempty"                     json:"pat,omitempty"`
	PATFromEnv            string `yaml:"patFromEnv,omitempty"              json:"patFromEnv,omitempty"`
	BaseURL               string `yaml:"baseUrl,omitempty"                 json:"baseUrl,omitempty"`
	CABundlePEM           string `yaml:"caBundlePem,omitempty"             json:"caBundlePem,omitempty"`
	InsecureSkipTLSVerify bool   `yaml:"insecureSkipTlsVerify,omitempty"   json:"insecureSkipTlsVerify,omitempty"`
}

// PackageSpec declares a catalog entry. UpstreamCredential references
// UpstreamCredentialSpec.Name; the reconciler resolves it to the credential's
// UUID at apply time.
//
// Multi-container packages set Containers (one per container alias). When
// Containers is non-empty, UpstreamRepo on the package itself is ignored
// (the reconciler writes an empty string) and the per-container UpstreamRepo
// values take over. All containers under a package share the package's
// UpstreamCredential.
type PackageSpec struct {
	Slug                  string          `yaml:"slug"                              json:"slug"`
	Source                string          `yaml:"source,omitempty"                  json:"source,omitempty"` // oci | github-release; defaults to "oci"
	Path                  string          `yaml:"path,omitempty"                    json:"path,omitempty"`
	UpstreamRepo          string          `yaml:"upstreamRepo,omitempty"            json:"upstreamRepo,omitempty"` // legacy single-container; ignored when Containers is non-empty
	Containers            []ContainerSpec `yaml:"containers,omitempty"              json:"containers,omitempty"`   // multi-container; takes precedence over UpstreamRepo
	GitHubRepo            string          `yaml:"githubRepo,omitempty"              json:"githubRepo,omitempty"`
	ReleasePattern        string          `yaml:"releasePattern,omitempty"          json:"releasePattern,omitempty"`
	AssetPattern          string          `yaml:"assetPattern,omitempty"            json:"assetPattern,omitempty"`
	UpstreamCredential    string          `yaml:"upstreamCredential"                json:"upstreamCredential"`
	Kind                  string          `yaml:"kind"                              json:"kind"` // container | helm | binary
	DisplayName           string          `yaml:"displayName,omitempty"             json:"displayName,omitempty"`
	Description           string          `yaml:"description,omitempty"             json:"description,omitempty"`
	ReleaseNotesURL       string          `yaml:"releaseNotesUrl,omitempty"         json:"releaseNotesUrl,omitempty"`
	InstallInstructionsMD string          `yaml:"installInstructionsMD,omitempty"   json:"installInstructionsMD,omitempty"`
}

// ContainerSpec declares one container under a multi-container package. Alias
// is a single path segment (no '/') used as the customer-facing URL suffix:
// dl.cnak.us/<package.path>/<alias>. UpstreamRepo names the upstream OCI repo
// the proxy pulls from; the credential is inherited from the parent package.
type ContainerSpec struct {
	Alias        string `yaml:"alias"                  json:"alias"`
	UpstreamRepo string `yaml:"upstreamRepo"           json:"upstreamRepo"`
	DisplayName  string `yaml:"displayName,omitempty"  json:"displayName,omitempty"`
}

// LicenseSpec is a raw .lic blob plus optional manifest-managed metadata such
// as the federated-login contact allowlist. The cnaklic license ID is
// extracted by the reconciler from the signed payload.
type LicenseSpec struct {
	LicBlob  string        `yaml:"licBlob"            json:"licBlob"`
	Contacts []ContactSpec `yaml:"contacts,omitempty" json:"contacts,omitempty"`
}

// ContactSpec is one federated-login contact for a license. Email is matched
// case-insensitively after canonical-lowering at apply time.
type ContactSpec struct {
	Email string `yaml:"email"          json:"email"`
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
}

// GrantSpec ties a license (by cnaklic licenseID, NOT the DB row UUID) to a
// set of package slugs and a set of actions. Empty Actions defaults to
// ["pull"] in the reconciler.
type GrantSpec struct {
	License  string   `yaml:"license"            json:"license"`
	Packages []string `yaml:"packages"           json:"packages"`
	Actions  []string `yaml:"actions,omitempty"  json:"actions,omitempty"`
}
