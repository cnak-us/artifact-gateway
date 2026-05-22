# artifact-gateway — license-gated downloads for non-OCI artifacts

Companion to the OCI `/v2/*` flow: this surface gates **GitHub Releases** and
**GitLab Releases** asset downloads behind the same `tokenId:secret` Basic
credentials and per-license grants customers already use for `docker pull`.

Bytes never traverse the gateway — every successful download is a `302` to the
upstream's short-lived signed CDN URL. The upstream PAT is never exposed to
the customer.

## Endpoints

| Method + path | Auth | Purpose |
|---|---|---|
| `GET /download/{slug}` | Basic OR catalog session | List the package's releases + assets. Each asset's `download_url` points back at the asset endpoint below. |
| `GET /download/{slug}/{tag}/{asset}` | Basic OR catalog session | Resolve the asset and `302` to the upstream signed URL. |
| `POST /catalog/api/downloads/sign` | catalog session | Mint a short-lived JWT bound to `/download/{slug}/{tag}/{asset}`. Returns `{ url, expires_in }`. |
| `GET /download/_signed/{token}` | none (the JWT *is* the authorization) | Consume the JWT, re-resolve the asset, `302`. |

The signed-URL flow exists for the browser path: a `Click to download` button
in the catalog can't prompt for Basic credentials cleanly, so the server mints
a JWT (TTL `90s`) and the page navigates to `/download/_signed/{token}`. The
JWT's `path` claim is authoritative — the token, not any URL parameter,
determines what's served.

All four endpoints share the same gate: load token (or session) → bcrypt-verify →
load license → license not revoked/expired → `HasGrant(license, package, "pull")`.
The OCI `pull` action is reused for download entitlement; there is no separate
`download` action.

## Package model

A package row is a download source when `packages.source` is one of:

- `github-release` — backed by the GitHub Releases API.
- `gitlab-release` — backed by the GitLab Releases API (self-hosted GitLab is
  supported via `upstream_credentials.base_url`).

Relevant columns on `packages`:

| Column | Meaning |
|---|---|
| `source` | `oci` (default), `github-release`, or `gitlab-release`. |
| `github_repo` | `owner/repo` for both vendors (the name is historical). |
| `release_pattern` | `latest` returns only the most recent release; anything else lists up to 20 releases. |
| `asset_pattern` | Comma-separated globs (`path.Match` semantics: `*`, `?`, character classes). Blank allows everything. |
| `upstream_credential_id` | Must point at an `upstream_credentials.kind` of `github-api` (for `github-release`) or `gitlab-api` (for `gitlab-release`). |

The admin UI exposes Source as a radio on `/admin/packages`; when GitHub /
GitLab Release is selected, the form swaps to show `github_repo`,
`release_pattern`, `asset_pattern`, and a credential picker filtered to the
matching `*-api` kind.

## Upstream credential scopes

| Kind | Scope |
|---|---|
| `github-api` | Classic PAT with `repo` (private) or `public_repo` (public). Fine-grained: Contents=Read, Metadata=Read. |
| `gitlab-api` | PAT or Project Access Token with `read_api` (and `read_repository` for private projects). `base_url` defaults to `https://gitlab.com`; set it to a self-hosted host. |

Both are listed (alongside the OCI kinds) in the upstream credentials section
of [`README.md`](README.md) and [`DEPLOYMENT.md`](DEPLOYMENT.md).

## Listing cache

Releases are cached in-memory per `(package_id, release_pattern)` for `60s`.
The cache lives in the gateway process — there is no `release_assets_cache`
table. Restarts and rollouts clear it. With single-replica deployments
(the documented topology) this keeps upstream rate-limit pressure bounded
without coordination.

## Signed download URLs

`POST /catalog/api/downloads/sign` mints a JWT (signed with `JWT_SIGNING_KEY`)
containing the customer's subject and the download path. The URL returned to
the browser is `/download/_signed/<jwt>`; following it consumes the token,
re-validates the path against the package row, and `302`s the user. TTL is
`90s` — long enough to absorb a click→navigate hop, short enough that a
URL leak isn't a long-lived bearer token.

Signed tokens are **stateless** (pure JWT). There is no database table to
sweep; expired tokens fail signature verification on use.

## Failure modes

| Scenario | Customer response | Notes |
|---|---|---|
| Missing/invalid Basic | `401` + `WWW-Authenticate: Basic realm="artifact-gateway"` | Metric: `downloads_total{outcome="unauthorized"}`. |
| Token revoked or expired | `401` | |
| License expired/revoked | `403` | |
| No grant on package | `403` | |
| Package is not a release source | `404` | E.g. someone calls `/download/<some-oci-slug>`. |
| Tag or asset not found upstream | `404` | |
| Asset name not allowed by `asset_pattern` | `404` | Deliberately indistinguishable from upstream-404 to avoid pattern leaks. |
| Upstream rate limit | `429` + `Retry-After` echoed from upstream | Metric: `downloads_total{outcome="rate_limited"}`. Gauge: `github_api_rate_limit_remaining`. |
| Upstream 5xx / network error | `502` | |
| Signed JWT invalid or expired | `401` from `/download/_signed/{token}` | |

## Metrics

Namespace `artifact_gateway`:

| Metric | Type | Labels |
|---|---|---|
| `downloads_total` | Counter | `source` (`github` / `registry`), `outcome` (`success`, `unauthorized`, `not_entitled`, `upstream_error`, `rate_limited`) |
| `download_signed_urls_issued_total` | Counter | — |
| `github_api_requests_total` | Counter | `result` (`success`, `error`, `rate_limited`) |
| `github_api_rate_limit_remaining` | Gauge | — |
| `gitlab_api_requests_total` | Counter | `result` (`ok`, `upstream_error`) |

Audit: every `302` emits a `LogPackagePull` event with `actor=tokenID|email`,
`resource=<package path>`, and the `tag/asset` in `details`. `POST .../sign`
emits a `sign-download` action.

## Implementation map

| Surface | File |
|---|---|
| HTTP handlers | `server/downloads.go` |
| GitHub Releases client | `server/github_releases.go` |
| GitLab Releases client | `server/gitlab_releases.go` |
| Admin `packages` API | `server/admin.go` |
| Schema | `store/schema.sql` (`packages.source`, `*_api` credential kinds) |
| Admin UI (source radio) | `ui/src/src/pages/admin/Packages.jsx` |
| Catalog UI (download buttons) | `ui/src/src/pages/catalog/PackageDetail.jsx` |
| Tests | `server/downloads_ginkgo_test.go`, `server/downloads_asset_pattern_test.go` |

## Open questions

- **Range / resume on signed CDN URLs.** We do not proxy bytes, so range
  handling depends entirely on whether the upstream's signed URL backend
  (S3 / Azure Blob / GitLab object store) honors `Range`. S3 generally does;
  Azure backends are less reliable. No server-side mitigation is implemented.
- **Rate-limit accounting under multi-replica.** `github_api_rate_limit_remaining`
  is a per-replica gauge; a shared PAT across replicas will flap. Acceptable
  for the documented single-replica topology, would need a NATS-backed
  shared counter if HPA is enabled.
- **Checksum surfacing.** A `checksums.txt` sidecar is treated as a normal
  asset; the gateway does not parse it or fold `sha256` values into the
  listing response. Customers verify post-download with the standard
  `sha256sum -c` flow.
