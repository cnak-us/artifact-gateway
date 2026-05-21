# artifact-gateway — license-gated downloads for non-OCI artifacts

Companion design to `/Users/wcrum/.claude/plans/linked-exploring-sutton.md` (OCI flow). Read that first; this doc only covers the **non-OCI** path: per-platform binaries and the compose bundle, both shipped as GitHub Release assets on `github.com/cnak-us/cnak`.

Status: design only. v1 = GitHub Releases; v2 = generic HTTPS.

---

## 1. Goals & non-goals

**In scope**
- Gate downloads of `cnak-{os}-{arch}.{tar.gz,zip}`, `cnakcli-*`, and `cnak-compose.tar.gz` behind the same `tokenId:secret` Basic credential customers already use for `docker pull`.
- Surface those downloads in the catalog UI with copy-pasteable curl snippets, browser download buttons, and parsed checksums.
- Single audit + metrics surface that mirrors the OCI side.
- Zero egress on artifact-gateway: passthrough the upstream signed URL.

**Out of scope (deferred)**
- Self-hosted artifact mirrors (S3/R2 buckets owned by us). Re-evaluate if GitHub rate-limit or availability becomes a constraint.
- Signed-binary verification (cosign/notary attestations). Checksums-only in v1; we publish what GH publishes.
- Customer-uploaded artifacts. This is download-only.
- Tarball *contents* introspection (we proxy the bytes, we don't crack tarballs open).
- Replacing the OCI Helm chart distribution. Helm charts remain OCI artifacts; only loose binaries + compose bundles use this path.

---

## 2. Source channels in scope

| Channel | v1 | v2 | Why |
|---|---|---|---|
| GitHub Releases | YES | — | This is where CNAK already ships (`release-binary.yml`, `release-compose.yml`). Zero migration cost. |
| Generic HTTPS + Basic | — | YES | Customers occasionally want to gate a partner's binary; one Go fetcher, no new infra. |
| S3/R2 presigned | no | no | Only matters if we move off GH Releases. Not a current need. |
| Cloudflare R2 + Worker | no | no | Same as above. |
| GH Packages (npm/maven/nuget) | no | no | Not a raw-binary store. Confirmed: these are language-package registries; loose tarballs aren't a first-class object. |

**Recommendation:** ship GitHub Releases support only in v1. The compose bundle and all per-platform binaries already live there. v2 is a one-handler add-on if a customer asks.

---

## 3. Data model changes

### Decision: extend `packages`, do not split into a new table

A second `release_packages` table would force every UI surface, audit helper, grant resolver, and admin form to branch by table. The package-grant model already keys on `package_id` and works regardless of source. One `packages` row per logical product (e.g. `cnak`, `cnakcli`, `cnak-compose`) is the natural unit.

What changes:

1. Extend `packages.kind` enum to include `"binary"` (already present) and `"compose"`. The existing `binary` value covered OCI-packaged binaries; we keep that meaning and let `source` discriminate.
2. Add a `source` enum to `packages` with values `oci` | `github-release`. Default `oci` for backward compatibility (all existing rows are OCI).
3. Make `upstream_repo`, `upstream_credential_id` semantics polymorphic by `source`:
   - `source='oci'`: `upstream_repo` = full ghcr path; credential is a ghcr PAT.
   - `source='github-release'`: `upstream_repo` = `owner/repo` (e.g. `cnak-us/cnak`); credential is a GitHub API PAT.
4. New nullable columns on `packages` for the release configuration (only meaningful when `source='github-release'`):
   - `release_tag_pattern TEXT` — `latest`, a specific tag (`v1.4.2`), or a semver constraint (`>=1.0.0 <2.0.0`).
   - `asset_pattern TEXT` — glob (`cnak-*-*.tar.gz`) or explicit comma-list. Used both for discovery (`GET /download/{slug}`) and to gate which asset names are downloadable.
   - `checksum_asset_name TEXT` — name of the checksums sidecar (default `checksums.txt`).
5. New table `release_assets_cache` for short-lived listing cache (avoid hammering the GH API on every catalog hit):
   - `(package_id, tag) → assets_json JSONB, fetched_at TIMESTAMPTZ`
   - TTL: 5 min for `latest`, 24 h for pinned tags. Invalidate on admin save.

### Decision: split `upstream_credentials.kind`, do not share a PAT across `ghcr` and `github-release`

Two reasons:
- **Scope minimization.** A GHCR pull PAT can be classic + `read:packages` only. A Releases API PAT must include `repo` (private repos require it; `public_repo` is not enough if the cnak repo is ever flipped private). Sharing one PAT forces the broader scope on both.
- **Blast radius.** Rotating one credential without affecting the other is a routine ops task.

Add `kind='github-api'` as a new valid value. Existing `kind='ghcr'` rows are untouched.

### DDL fragment (next migration `00002_downloads.sql`)

```sql
-- +goose Up
-- +goose StatementBegin

-- 1. New credential kind: 'github-api' (PAT with `repo` for private + Releases API)
ALTER TABLE upstream_credentials
    DROP CONSTRAINT IF EXISTS upstream_credentials_kind_check;
ALTER TABLE upstream_credentials
    ADD CONSTRAINT upstream_credentials_kind_check
        CHECK (kind IN ('ghcr', 'github-api'));

-- 2. Extend packages with source + release-config
ALTER TABLE packages
    ADD COLUMN source TEXT NOT NULL DEFAULT 'oci'
        CHECK (source IN ('oci', 'github-release')),
    ADD COLUMN release_tag_pattern TEXT NOT NULL DEFAULT '',
    ADD COLUMN asset_pattern TEXT NOT NULL DEFAULT '',
    ADD COLUMN checksum_asset_name TEXT NOT NULL DEFAULT 'checksums.txt';

-- 3. Allow new package kind 'compose'
ALTER TABLE packages DROP CONSTRAINT packages_kind_check;
ALTER TABLE packages
    ADD CONSTRAINT packages_kind_check
        CHECK (kind IN ('container', 'helm', 'binary', 'compose'));

-- 4. Listing cache (5 min for `latest`, 24h for pinned)
CREATE TABLE release_assets_cache (
    package_id   UUID NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    tag          TEXT NOT NULL,
    assets_json  JSONB NOT NULL,
    fetched_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (package_id, tag)
);

-- 5. Signed download tokens (browser flow)
CREATE TABLE download_signed_tokens (
    token        TEXT PRIMARY KEY,            -- 32-byte url-safe random
    package_id   UUID NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    license_id   UUID NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    tag          TEXT NOT NULL,
    asset_name   TEXT NOT NULL,
    issued_to    TEXT NOT NULL DEFAULT '',    -- catalog session subject for audit
    expires_at   TIMESTAMPTZ NOT NULL,         -- typ. now() + 60s
    consumed_at  TIMESTAMPTZ NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX download_signed_tokens_expires_idx ON download_signed_tokens (expires_at);
-- +goose StatementEnd

-- +goose Down — drop in reverse, restore old check constraints
```

Old `Package` struct in `store/store.go` gains:

```go
Source              string  // "oci" | "github-release"
ReleaseTagPattern   string  // "latest" | tag | semver expr
AssetPattern        string  // glob or csv
ChecksumAssetName   string  // default "checksums.txt"
```

---

## 4. Auth surface

Three new endpoints. All license-gating is identical to `/v2/token`: load token → bcrypt-compare → load license → `license.Parse` + `IsExpired` → check `HasGrant(license, package, "download")`. (Reuse the existing `pull` action; rename to `pull|download` semantics in the resolver since the OCI side already says "actions[]". Cheaper than introducing a new action.)

### 4.1 `GET /download/{slug}/{tag}/{asset}` — CLI flow (Basic)

Customer hits this with `curl -u tokenId:secret -L -o cnak.tar.gz ...`.

| Step | Behavior |
|---|---|
| Missing/invalid `Authorization: Basic` | `401` + `Www-Authenticate: Basic realm="artifact-gateway"`. Metric: `download_requests_total{result="unauthorized"}`. |
| Token revoked / not found | `401`. |
| License expired/revoked | `403` `denied: license-expired`. |
| No grant on package | `403` `denied: no-grant`. |
| `{tag}` not resolvable (e.g. `latest` → no releases) | `404 release-not-found`. |
| `{asset}` not in resolved release | `404 asset-not-found`. |
| Asset name doesn't match `asset_pattern` | `404 asset-not-allowed` (same code, deliberately indistinguishable from upstream-404 to avoid pattern leaks). |
| Upstream 429 | `429` + `Retry-After` echoed (or `60` default). Metric: `download_upstream_errors_total{kind="rate_limit"}`. |
| Upstream 5xx | `502 upstream-error`. |
| Success | `302` to GH's signed URL. **The PAT is NOT included on this hop** — we strip `Authorization` from our response. Modern curl (≥7.58) handles this correctly; we document the minimum curl version in the catalog UI snippet. |

`{tag}` resolution:
- Literal tag (`v1.4.2`) → call `GET /repos/{owner}/{repo}/releases/tags/{tag}`.
- `latest` → call `GET /repos/{owner}/{repo}/releases/latest`.
- Semver expression in `release_tag_pattern` → fetch first page of `GET /repos/{owner}/{repo}/releases`, pick highest matching tag.
- Results cached per-package per-tag in `release_assets_cache`.

Server-side outbound call: `GET /repos/{owner}/{repo}/releases/assets/{asset_id}` with `Accept: application/octet-stream` and `Authorization: Bearer <PAT>`. GitHub returns `302` with a signed S3/Azure URL; we forward that `Location` to the customer verbatim. We do **not** follow the redirect ourselves (zero egress).

### 4.2 `POST /catalog/api/downloads/sign` — browser flow

Authenticated by the existing `ag_customer_session` cookie. Body:

```json
{ "slug": "cnak", "tag": "v1.4.2", "asset": "cnak-linux-amd64.tar.gz" }
```

Server validates license + grant identically to 4.1, inserts a `download_signed_tokens` row with TTL 60s, returns `{ "url": "/download/_signed/<token>", "expires_at": "..." }`. UI does `window.location = url`.

`GET /download/_signed/{token}`:
- No Basic required. Looks up token, marks `consumed_at`, checks `expires_at`, re-checks license + grant (the license could have been revoked in the 60s window — fail closed), then proceeds exactly as 4.1's success path.
- One-shot: `consumed_at != NULL` → `410 Gone`.

Rationale for the two-handler split: browsers can't be prompted for Basic without an ugly native dialog, and we want the audit event tagged with the catalog session's user identity (not just the `token_id`). The signed-token table also gives us a clean record of who initiated each browser download.

### 4.3 `GET /download/{slug}` — discovery

Auth: either Basic (CLI use, e.g. `curl -u ... /download/cnak | jq`) or `ag_customer_session` (catalog UI). Returns:

```json
{
  "slug": "cnak",
  "kind": "binary",
  "source": "github-release",
  "tags": [
    {
      "tag": "v1.4.2",
      "published_at": "2026-05-14T...",
      "release_notes_url": "https://github.com/cnak-us/cnak/releases/tag/v1.4.2",
      "assets": [
        {
          "name": "cnak-linux-amd64.tar.gz",
          "size": 47185920,
          "sha256": "ab12...",
          "content_type": "application/gzip"
        }
      ]
    }
  ]
}
```

Tag list bounded by `release_tag_pattern` (e.g. `latest` returns exactly one entry; a semver constraint returns all matching). Checksums are parsed from the `checksums.txt` sidecar at list time and folded into each asset entry. Cached in `release_assets_cache`.

---

## 5. Redirect / streaming policy

**Default: passthrough 302.** Mirrors the OCI blob-passthrough rationale:
- GH's signed URL is opaque and short-lived (per docs and field reports, on the order of single-digit minutes — short enough to be safe to relay, long enough to start a download).
- No GH PAT goes to the customer at any point.
- Zero egress through artifact-gateway.

**Range / resume.** GitHub Release assets redirect to AWS S3 (or Azure Blob for some GHE setups). Both backends generally honor `Range` on pre-signed GETs, but GitHub's REST docs do not contractually guarantee it. Stance:

1. **Default behavior: passthrough.** The customer's curl/browser will negotiate `Range` directly with S3. If it works, great — zero work for us.
2. **If a customer reports broken resume**, add a stream-through fallback gated by `DOWNLOAD_STREAM_RANGE=true`: when the inbound request has a `Range` header, the gateway follows the 302 itself, opens an upstream `Range` GET against the signed URL, and streams bytes back. Adds egress, so it's opt-in.
3. Add an open question to §10 to do a one-shot empirical test against a real GH release before v1 ships.

**Checksums.** Each CNAK release has `checksums.txt` as a sibling asset (per `create-checksums` job in `release-binary.yml`). We:
- Treat it as a first-class asset of the package (`checksum_asset_name` field).
- Fetch + parse it during the discovery cache population; expose `sha256` per asset in `GET /download/{slug}`.
- Serve it via the same `/download/{slug}/{tag}/checksums.txt` path so customers can verify post-download.

---

## 6. UI changes (for `frontend-builder`)

Catalog package detail page (`/catalog/p/:slug`) — current behavior: render `docker pull` / `helm pull` snippets from `install_instructions_md`.

**New behavior when `package.source === 'github-release'`** (or `kind ∈ {binary, compose}` if you want the simpler conditional):

1. Replace install-snippet block with a **Downloads** section, two parts:

   **Tag selector** (defaults to first entry from `GET /download/{slug}`). When `release_tag_pattern === 'latest'`, hide the selector and show "latest (v1.4.2)" as a label.

   **Assets table**:

   | Platform | Arch | Size | SHA256 | Actions |
   |---|---|---|---|---|
   | Linux | amd64 | 45 MB | `ab12…` (click to copy) | [Copy curl] [Download] |
   | Linux | arm64 | … | … | … |
   | macOS | amd64 | … | … | … |
   | macOS | arm64 | … | … | … |
   | Windows | amd64 | … | … | … |

   Platform/arch parsed from asset name with a regex (`/-(linux|darwin|windows)-(amd64|arm64)/`). Unknown patterns fall through to a single "Other" row.

2. **[Copy curl]** copies:

   ```
   curl -u <TOKEN_ID>:<YOUR_SECRET> -L \
     -o cnak-linux-amd64.tar.gz \
     https://<HOSTNAME>/download/cnak/v1.4.2/cnak-linux-amd64.tar.gz
   ```

   Substitute the customer's actual `token_id` from the catalog session. Leave `<YOUR_SECRET>` literal (we don't have the cleartext) with a tooltip "use the secret you saved when creating this token."

3. **[Download]** calls `POST /catalog/api/downloads/sign` with the slug/tag/asset, then does `window.location.assign(resp.url)`.

4. **Compose bundle** (`kind === 'compose'`): above the assets table, show a "Quick start" code block:

   ```bash
   curl -u <TOKEN_ID>:<YOUR_SECRET> -L \
     -o cnak-compose.tar.gz \
     https://<HOSTNAME>/download/cnak-compose/latest/cnak-compose.tar.gz
   tar xzf cnak-compose.tar.gz
   cd cnak-compose
   cp .env.example .env
   # edit .env, then:
   docker compose up -d
   ```

5. Verification helper: under each asset row, a "Verify" expandable showing:

   ```bash
   echo "<sha256>  cnak-linux-amd64.tar.gz" | sha256sum -c -
   ```

Admin UI (`/admin/packages`) gains a "Source" radio (OCI / GitHub Release). When GitHub Release is selected, the form swaps to show fields for `upstream_repo` (e.g. `cnak-us/cnak`), `release_tag_pattern`, `asset_pattern`, `checksum_asset_name`, and a credential picker filtered to `kind='github-api'`.

---

## 7. Metrics + audit

New Prometheus metrics, namespace `artifact_gateway`, parallel to OCI:

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `download_requests_total` | Counter | `result` (success / unauthorized / denied_license / denied_grant / not_found / upstream_error / rate_limit) | Counts both CLI Basic and signed-token paths. |
| `download_request_latency_seconds` | Histogram | `result` | Buckets `.01 .05 .1 .25 .5 1 2.5`. End-to-end from request in to 302 out. |
| `download_redirect_bytes_total` | Counter | — | From upstream `Content-Length` on the asset metadata; this is implied bytes since the actual transfer goes around us. |
| `download_upstream_errors_total` | Counter | `kind` (rate_limit / 5xx / auth / dns / timeout) | |
| `download_listing_cache_hits_total` | Counter | `result` (hit/miss/stale) | |
| `download_signed_tokens_issued_total` | Counter | — | |
| `download_signed_tokens_expired_total` | Counter | — | Background sweeper increments. |
| `gh_api_rate_limit_remaining` | Gauge | `credential_id` | Pulled from `x-ratelimit-remaining` on every outbound call; lets us alert before we hit the wall. |

New audit helpers in `audit/audit.go`, mirroring `LogPackagePull`:

```go
func (a *Auditor) LogPackageDownload(actor, packagePath, tag, asset, ip, status string)
func (a *Auditor) LogDownloadSign(actor, packagePath, tag, asset, ip string)  // who clicked the browser button
```

`actor` for the Basic path is the `token_id`; for the signed path it's the catalog session subject (currently == `token_id`, but defined for future drift).

---

## 8. Failure modes

| Scenario | Detection | Customer-facing response | Operator-facing signal |
|---|---|---|---|
| GitHub primary rate limit (5000/hr) | `x-ratelimit-remaining: 0` or `403` with `rate limit exceeded` | `429` + `Retry-After: 60` | `gh_api_rate_limit_remaining` gauge < 100 → Prom alert; log line at WARN. |
| GitHub secondary rate limit (concurrent / per-min) | `403` with `secondary rate limit` | `429` + `Retry-After` from upstream header | `download_upstream_errors_total{kind="rate_limit"}` counter. |
| GH PAT expired/revoked | `401 Bad credentials` on outbound | `502 upstream-auth-failed` | Audit `download_upstream_errors_total{kind="auth"}` → on first occurrence, mark `upstream_credentials.last_used_at` with a status field (new col? or a dedicated `upstream_credentials_health` row — design choice for the implementer). |
| Asset deleted upstream | `404` from `releases/assets/{id}` | `404 asset-not-found`, invalidate `release_assets_cache` row | log line; next discovery call will see the gap. |
| Tag deleted upstream | `404` from `releases/tags/{tag}` | `404 release-not-found` | log. |
| Signed S3 URL expired between issue and customer use | Customer gets a 403 from S3 directly | We don't see this — it's after the 302 | Mitigate by issuing fresh signed URLs (i.e. don't cache the *signed* URL — only cache the `asset_id` listing). The discovery cache stores asset metadata, not signed URLs. |
| Listing cache stampede on `latest` | Many concurrent /download/{slug} | One go-routine populates, others wait via `singleflight` | `download_listing_cache_hits_total{result="stale"}`. |
| Customer Range request not honored by S3 backend | Reported by user, not detectable server-side | Flip `DOWNLOAD_STREAM_RANGE=true` per-deployment | Open question §10. |
| Customer using `curl < 7.58.0` and gets Authorization passed to S3 | Manifests as `400 Bad Request` from S3 | Documented in catalog UI snippet ("requires curl 7.58 or newer; on RHEL7 use `--remove-on-error -H Authorization:`") | Cite the gist in admin docs. |

---

## 9. Implementation task breakdown

Each task is sized for an implementer to claim independently. Reference `DOWNLOADS.md` in each.

1. **Schema + store extension** (~1 day)
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/store/migrations/00002_downloads.sql` (DDL in §3)
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/store/store.go` — extend `Package` struct; add `ReleaseAssetCache` and `DownloadSignedToken` types + interface methods (`GetReleaseAssetCache`, `UpsertReleaseAssetCache`, `IssueSignedDownloadToken`, `ConsumeSignedDownloadToken`, `SweepExpiredSignedTokens`).
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/store/pg.go` — pgx implementations.
   - Tests: extend `store/*_ginkgo_test.go` for new methods.
   - Dependencies: none.

2. **GitHub Releases upstream client** (~1 day)
   - File: new `/Users/wcrum/Coding/cnak-us/artifact-gateway/upstream/github/releases.go`
   - Signatures:
     ```go
     type Client struct { /* http.Client + base URL + PAT */ }
     func New(baseURL string, pat string) *Client
     func (c *Client) ResolveTag(ctx, owner, repo, pattern string) (Release, error)
     func (c *Client) ListReleases(ctx, owner, repo string) ([]Release, error)
     func (c *Client) GetAssetRedirect(ctx, assetID int64) (location string, err error) // returns 302's Location; never follows
     func (c *Client) FetchChecksums(ctx, owner, repo, tag, assetName string) (map[string]string, error)
     ```
   - Reads `x-ratelimit-remaining` and updates `metrics.GHApiRateLimitRemaining`.
   - Uses `singleflight` for the discovery cache populator.
   - Dependencies: task 1 (for cache writes).

3. **Download HTTP handlers** (~2 days)
   - File: new `/Users/wcrum/Coding/cnak-us/artifact-gateway/server/download.go` — routes:
     - `GET /download/{slug}` — discovery (Basic OR session)
     - `GET /download/{slug}/{tag}/{asset}` — CLI Basic flow
     - `POST /catalog/api/downloads/sign` — issue signed token (session)
     - `GET /download/_signed/{token}` — consume signed token (no auth)
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/server/server.go` — register routes; reuse existing Basic middleware from `/v2/token`.
   - Background sweeper for expired signed tokens: 5-min ticker calling `SweepExpiredSignedTokens`.
   - Tests: `server/download_ginkgo_test.go` — license expired → 403, ungranted → 403, asset not in pattern → 404, success → 302 with empty Authorization on response.
   - Dependencies: tasks 1 + 2.

4. **Admin API + UI for github-release packages** (~1.5 days)
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/server/admin.go` — extend `POST/PATCH /api/v1/packages` to accept new fields; validate `source='github-release'` → require `release_tag_pattern`, `asset_pattern`, and a credential of `kind='github-api'`.
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/ui/src/src/pages/admin/Packages.jsx` — source radio, conditional field group.
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/ui/src/src/pages/admin/UpstreamCredentials.jsx` — allow `kind='github-api'` in the picker.
   - Dependencies: tasks 1 + 2.

5. **Catalog UI downloads block** (~1.5 days)
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/ui/src/src/pages/catalog/PackageDetail.jsx` — render §6 downloads table + curl copy + signed-URL download button + verify helper.
   - File: `/Users/wcrum/Coding/cnak-us/artifact-gateway/ui/src/src/api/client.js` — `getDownloadListing(slug)`, `signDownload(slug, tag, asset)`.
   - Dependencies: task 3.

6. **(v2, optional) Generic-HTTPS-Basic source** (~1 day)
   - Add `source='https-basic'` to the enum; new `upstream_credentials.kind='http-basic'`; new fields `https_base_url`, asset list registered explicitly (no upstream discovery API for arbitrary HTTPS).
   - One new branch in `upstream/` and one new conditional in `download.go`.
   - Defer until a customer asks.

---

## 10. Open questions

1. **Range/resume on GH's signed URLs.** Docs are silent; community reports suggest S3 backend honors `Range`, Azure backend (GHES) sometimes does not. **Action before v1 ships:** publish a tiny test asset to a sandbox repo, `curl -H 'Range: bytes=0-1023'` against the resulting signed URL, confirm `206 Partial Content`. Codify the result here and decide whether to ship `DOWNLOAD_STREAM_RANGE` as opt-in or always-on. ([How to download an asset? — community discussion](https://github.com/orgs/community/discussions/136830), [Download assets from private Github releases — gist](https://gist.github.com/maxim/6e15aa45ba010ab030c4))
2. **One PAT for both `repo` + `read:packages`?** Yes — a classic PAT can hold both scopes simultaneously, so an admin who wants to share could do so. But we still recommend split credentials per §3 (blast radius). Confirm with `gh auth status` after creating a dual-scoped token.
3. **Listing cache TTL for `latest`.** 5 min chosen by analogy to similar caches; verify against customer expectation that a freshly-published release shows up quickly. If 5 min feels slow, add a "refresh" button in the admin UI that invalidates `release_assets_cache` for one package.
4. **GH API hostname.** Hard-code `https://api.github.com` or expose `UPSTREAM_GITHUB_API` env var? Recommend the env var so we can point at GHES later without code changes. Coordinate with `deploy-architect` (already messaged).
5. **Compose-bundle "kind".** Could be `compose` or stay as `binary` and discriminate in the UI on filename suffix. Recommend `compose` because the UI treatment differs materially (quick-start block, no platform/arch parse).
6. **Signed-token TTL.** 60s default; the customer's browser races between `sign` response and `Location` follow. If we see `download_signed_tokens_expired_total` climbing, raise to 120s. Don't go higher — long-lived bearer-in-URL tokens are a footgun.
7. **Rate-limit accounting per credential.** A single PAT shared across many gateway instances would multiply hits. If we deploy multi-replica, the `gh_api_rate_limit_remaining` gauge will flap between replicas. Consider a NATS-backed shared counter in v2 — out of scope for v1 (single replica is the documented topology).

---

## References

- GitHub REST API rate limits — [docs.github.com/en/rest/overview/rate-limits-for-the-rest-api](https://docs.github.com/en/rest/overview/rate-limits-for-the-rest-api) — 60/hr anon, 5000/hr authenticated, secondary limits at 900 points/min and 100 concurrent.
- Release assets endpoint — [docs.github.com/en/rest/releases/assets](https://docs.github.com/en/rest/releases/assets) — `Accept: application/octet-stream` + may 200 or 302.
- Private release asset download patterns — [gist.github.com/maxim/6e15aa45ba010ab030c4](https://gist.github.com/maxim/6e15aa45ba010ab030c4) — confirms older curl leaks Authorization to S3/Azure redirect.
- Asset download in practice — [community discussion 136830](https://github.com/orgs/community/discussions/136830) — confirms 302 to short-lived signed URL.
- Fine-grained vs classic PATs — [github.blog fine-grained PATs](https://github.blog/security/application-security/introducing-fine-grained-personal-access-tokens-for-github/) — per-repo scoping, expiration, recommended over classic.
