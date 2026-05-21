// Package apply implements the declarative ArtifactGatewayConfig custom
// resource: parsing, env-resolution, and reconciliation of an entire gateway
// configuration document against the database.
//
// The CR shape mirrors a Kubernetes-style resource so admins can apply the
// same YAML through the UI, via `kubectl create -f`-style tooling, or mount
// it at startup via the CONFIG_FILE env var.
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
type ManifestSpec struct {
	StaticAdmins        []StaticAdminSpec        `yaml:"staticAdmins,omitempty"        json:"staticAdmins,omitempty"`
	OIDCProviders       []OIDCProviderSpec       `yaml:"oidcProviders,omitempty"       json:"oidcProviders,omitempty"`
	UpstreamCredentials []UpstreamCredentialSpec `yaml:"upstreamCredentials,omitempty" json:"upstreamCredentials,omitempty"`
	Packages            []PackageSpec            `yaml:"packages,omitempty"            json:"packages,omitempty"`
	Licenses            []LicenseSpec            `yaml:"licenses,omitempty"            json:"licenses,omitempty"`
	Grants              []GrantSpec              `yaml:"grants,omitempty"              json:"grants,omitempty"`
}

// StaticAdminSpec is a break-glass admin written to the static_admins table.
// Either Password or PasswordFromEnv must be set; resolve.go drains
// PasswordFromEnv into Password before the manifest reaches the reconciler.
type StaticAdminSpec struct {
	Email            string `yaml:"email"                      json:"email"`
	Password         string `yaml:"password,omitempty"         json:"password,omitempty"`
	PasswordFromEnv  string `yaml:"passwordFromEnv,omitempty"  json:"passwordFromEnv,omitempty"`
}

// OIDCProviderSpec declares an OIDC IdP. ClientSecret may be inline or read
// from the named env var via ClientSecretFromEnv (which resolve.go consumes).
type OIDCProviderSpec struct {
	Name                 string   `yaml:"name"                          json:"name"`
	IssuerURL            string   `yaml:"issuerUrl"                     json:"issuerUrl"`
	ClientID             string   `yaml:"clientId"                      json:"clientId"`
	ClientSecret         string   `yaml:"clientSecret,omitempty"        json:"clientSecret,omitempty"`
	ClientSecretFromEnv  string   `yaml:"clientSecretFromEnv,omitempty" json:"clientSecretFromEnv,omitempty"`
	Scopes               []string `yaml:"scopes,omitempty"              json:"scopes,omitempty"`
	Enabled              bool     `yaml:"enabled"                       json:"enabled"`
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
type PackageSpec struct {
	Slug                  string `yaml:"slug"                              json:"slug"`
	Source                string `yaml:"source,omitempty"                  json:"source,omitempty"` // oci | github-release; defaults to "oci"
	Path                  string `yaml:"path,omitempty"                    json:"path,omitempty"`
	UpstreamRepo          string `yaml:"upstreamRepo,omitempty"            json:"upstreamRepo,omitempty"`
	GitHubRepo            string `yaml:"githubRepo,omitempty"              json:"githubRepo,omitempty"`
	ReleasePattern        string `yaml:"releasePattern,omitempty"          json:"releasePattern,omitempty"`
	AssetPattern          string `yaml:"assetPattern,omitempty"            json:"assetPattern,omitempty"`
	UpstreamCredential    string `yaml:"upstreamCredential"                json:"upstreamCredential"`
	Kind                  string `yaml:"kind"                              json:"kind"` // container | helm | binary
	DisplayName           string `yaml:"displayName,omitempty"             json:"displayName,omitempty"`
	Description           string `yaml:"description,omitempty"             json:"description,omitempty"`
	ReleaseNotesURL       string `yaml:"releaseNotesUrl,omitempty"         json:"releaseNotesUrl,omitempty"`
	InstallInstructionsMD string `yaml:"installInstructionsMD,omitempty"   json:"installInstructionsMD,omitempty"`
}

// LicenseSpec is a raw .lic blob. The cnaklic license ID is extracted by the
// reconciler from the signed payload.
type LicenseSpec struct {
	LicBlob string `yaml:"licBlob" json:"licBlob"`
}

// GrantSpec ties a license (by cnaklic licenseID, NOT the DB row UUID) to a
// set of package slugs and a set of actions. Empty Actions defaults to
// ["pull"] in the reconciler.
type GrantSpec struct {
	License  string   `yaml:"license"            json:"license"`
	Packages []string `yaml:"packages"           json:"packages"`
	Actions  []string `yaml:"actions,omitempty"  json:"actions,omitempty"`
}
