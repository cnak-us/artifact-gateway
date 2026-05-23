# artifact-gateway — Deployment Story

This document is the canonical reference for shipping and operating
`artifact-gateway`.

## Surfaces we ship

| Surface                         | Ship?       | Rationale |
| ------------------------------- | ----------- | --------- |
| Docker image (multi-stage)      | **Yes**     | Primary deployment artifact. Distroless, ~30 MB, embedded UI. Published to `ghcr.io/cnak-us/artifact-gateway`. |
| Helm chart (OCI)                | **Yes**     | Kubernetes is the production target. Chart is published as an OCI artifact to `ghcr.io/cnak-us/artifact-gateway/chart` so it can be `helm install` and `helm pull oci://`. |
| Standalone Linux binary release | **No** (v1) | Service has hard runtime dependencies on Postgres and an externally terminated TLS cert. There is no scenario where a "just the binary" deploy is meaningfully easier than a one-pod K8s deploy or a 3-line `docker run`. Revisit only if a customer demonstrably needs an air-gapped, single-VM install. |

## Deployment modes

- **Kubernetes via Helm — primary.** Every production install. The
  chart at `chart/` is the single source of truth for the runtime
  shape (env, ports, probes, secrets).
- **Docker Compose — local dev only.** Lives under
  `docker-compose.yml` and brings up Postgres + the gateway. Not for
  production: it does no TLS termination.
- **Binary mode — out of scope.** Not built, not tested. Rationale
  under "Surfaces we ship". If we ever do this, it would be the
  binary, an external Postgres, and a reverse proxy in front for TLS.

## Required infrastructure

| Component            | Required?    | Notes |
| -------------------- | ------------ | ----- |
| **Postgres ≥ 14**    | **Required** | Single tenant DB. No bundled DB in the chart — bring your own (CloudNativePG, RDS, Cloud SQL, Bitnami subchart). The gateway creates `pgcrypto` and `citext` extensions and the full schema on startup against a fresh database. |
| **Ingress + TLS**    | **Required** | OCI clients (`docker`, `helm`, `oras`) refuse to talk to insecure registries on non-`localhost` hostnames. You need a real cert and a real ingress controller. |
| **cert-manager**     | **Recommended** | Easiest TLS source; the cert SAN constraint below is fragile to get right by hand. |
| **NATS**             | **Optional** | When configured, the gateway publishes audit events on `audit.{resource_type}.{action}` and listens for `cnak.internal.license.updated` to invalidate its license cache. Without NATS the gateway works, but license revocations take up to the cache TTL to propagate. |
| **Prometheus Operator** | Optional  | `ServiceMonitor` is rendered when `metrics.serviceMonitor.enabled=true`. |

## Environment variable matrix

Every var the binary reads, cross-referenced against `config/config.go` and the
direct `os.Getenv` calls in `main.go`.

| Var                       | Default                   | Source            | Secret? | Notes |
| ------------------------- | ------------------------- | ----------------- | ------- | ----- |
| `PUBLIC_PORT`             | `8080`                    | ConfigMap         | no      | OCI + admin + catalog listener |
| `MANAGEMENT_PORT`         | `8090`                    | ConfigMap         | no      | `/health/*` + `/metrics` |
| `EXTERNAL_HOSTNAME`       | `localhost:8080`          | ConfigMap         | no      | **MUST match the TLS cert SAN exactly** — see Cert/hostname constraint |
| `TOKEN_TTL_SECONDS`       | `300`                     | ConfigMap         | no      | OCI bearer JWT lifetime |
| `TLS_CERT_FILE`           | (empty)                   | not chart-wired   | no      | Set with `TLS_KEY_FILE` to enable in-process HTTPS on the public listener. Leave blank when an upstream LB/Cloudflare terminates TLS. Management listener stays HTTP. |
| `TLS_KEY_FILE`            | (empty)                   | not chart-wired   | no      | See `TLS_CERT_FILE`. |
| `COOKIE_SECURE`           | auto                      | not chart-wired   | no      | Secure flag on session cookies. Defaults to true; auto-falls-back to false when `EXTERNAL_HOSTNAME` points at localhost. |
| `OIDC_AUTOPROVISION`      | `false`                   | ConfigMap         | no      | When true, new OIDC users land as `role='viewer'` |
| `OIDC_DEFAULT_PROVIDER`   | `dex`                     | not chart-wired   | no      | Provider name the gateway redirects unauthenticated admin requests to. |
| `STATIC_ADMINS`           | (empty)                   | Secret            | **yes** | Comma-separated `email:password` pairs for break-glass / CI admins. Sessions issued for these entries bypass the DB. |
| `LOG_LEVEL`               | `info`                    | ConfigMap         | no      | `debug\|info\|warn\|error` |
| `LOG_FORMAT`              | `json`                    | ConfigMap         | no      | `json\|text` |
| `NATS_URL`                | (empty)                   | ConfigMap         | no      | Empty disables NATS publish + license cache invalidation |
| `NATS_CREDENTIALS_FILE`   | (empty)                   | Mounted file path | no      | Path to a `.creds` file mounted from `nats.credentialsSecret` |
| `NATS_AUTH_TOKEN`         | (empty)                   | Secret            | **yes** | Only used when NATS is password-authed |
| `POD_NAME`                | (empty)                   | Downward API      | no      | For HA cache key disambiguation |
| `DATABASE_URL`            | (empty)                   | Secret            | **yes** | `postgres://user:pass@host:5432/db?sslmode=require` |
| `KEK_BASE64`              | (empty)                   | Secret            | **yes** | 32 random bytes, base64. **AES-GCM KEK for stored upstream PATs and issuer secrets.** Losing this bricks everything at rest. |
| `SESSION_SIGNING_KEY`     | (empty)                   | Secret            | **yes** | Hex, ≥ 32 bytes. HMAC-SHA256 for admin/catalog cookies |
| `JWT_SIGNING_KEY`         | (empty)                   | Secret            | **yes** | Hex, ≥ 32 bytes. HMAC-SHA256 for OCI bearer JWTs |
| `SERVICE_TOKEN`           | (empty)                   | Secret            | **yes** | Shared secret for any internal service callers |
| `ADMIN_BOOTSTRAP_EMAIL`   | (empty)                   | Secret            | **yes** | One-time. Used only if no users exist at startup |
| `ADMIN_BOOTSTRAP_PASSWORD`| (empty)                   | Secret            | **yes** | One-time. Rotate after first login. |
| `DEX_ISSUER_URL`          | (empty)                   | ConfigMap         | no      | When set with `DEX_CLIENT_ID` + `DEX_CLIENT_SECRET`, the gateway auto-provisions a `dex` OIDC provider row on startup. |
| `DEX_CLIENT_ID`           | (empty)                   | ConfigMap         | no      | See `DEX_ISSUER_URL`. |
| `DEX_CLIENT_SECRET`       | (empty)                   | Secret            | **yes** | See `DEX_ISSUER_URL`. |
| `DEX_DISCOVERY_URL`       | (empty)                   | not chart-wired   | no      | Override the in-network discovery URL for the `dex` provider when it differs from the browser-visible issuer (compose dev). |
| `CONFIG_FILE`             | (empty)                   | not chart-wired   | no      | Path to a declarative-config YAML applied at startup. Hard-exits on error. Read directly by `main.go`; not exposed by the chart today. |

The chart enforces "secret → Secret, non-secret → ConfigMap" with
`envFrom:` on both. There is no inline `env:` other than `POD_NAME`
from the downward API. Vars marked **not chart-wired** are read by the
binary but the chart doesn't surface them as first-class values today
— set them by patching `templates/configmap.yaml` (non-secret) or
`templates/secret-env.yaml` (secret) in a values fork.

## Upstream credentials: required PAT scopes

Operators add one upstream credential per registry via the admin UI or
the config-apply manifest. The required token scope depends on the
credential `kind`:

| Kind         | Required scope / permission                                                                                       |
|--------------|-------------------------------------------------------------------------------------------------------------------|
| `ghcr`       | Classic PAT with `read:packages`. GHCR does not support fine-grained PATs for `docker pull`.                       |
| `github-api` | Classic PAT with `repo` (private) or `public_repo` (public). Fine-grained: Contents=Read, Metadata=Read. Used for GitHub Releases asset downloads (see `DOWNLOADS.md`). |
| `gitlab-api` | PAT or Project Access Token with `read_api` (and `read_repository` for private projects). Used for GitLab Releases asset downloads. `base_url` defaults to `https://gitlab.com`; set for self-hosted GitLab. |
| `oci-basic`  | Pull/read on the target repository. Gitea: `read:package`. Harbor: robot account with pull+read. Artifactory: Identity Token with repo read. ACR scope-mapped tokens also fit here. Self-hosted instances can paste an internal CA chain into `ca_bundle_pem`. |
| `dockerhub`  | Docker Hub PAT with Read scope (`hub.docker.com → Account Settings → Security`). Host is pinned to `registry-1.docker.io`. |
| `quay`       | Robot account name (`org+robotname`) and robot token with read permission on the target repos. Defaults to `quay.io`; set `base_url` for self-hosted Quay. |
| `gitlab`     | Project/Group Deploy Token (recommended) or PAT with `read_registry`. Set `base_url` to the registry host (`registry.gitlab.com` for SaaS, `registry.<your-domain>` for self-hosted). The proxy auto-discovers the JWT realm from the upstream's 401 challenge. |
| `ecr`        | IAM principal with `ecr:GetAuthorizationToken`, `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer`. Provide `issuer_secret={accessKeyId, secretAccessKey}` and `issuer_config={region, accountId}`. The proxy mints a 12-hour Basic credential and refreshes before expiry. |
| `gar`        | Google service-account JSON with `roles/artifactregistry.reader`. Provide `issuer_secret` (raw SA JSON) and `base_url` (the regional registry, e.g. `https://us-docker.pkg.dev`). The proxy mints a ~1h OAuth2 access token and refreshes before expiry. |
| `acr-aad`    | Azure service principal with AcrPull. Provide `issuer_secret={clientId, clientSecret}`, `issuer_config={tenantId, registry}`, and `base_url=https://<name>.azurecr.io`. The proxy does AAD → ACR refresh → ACR access exchange and refreshes the short-lived (~5 min) access token. |

## Secret model and KEK rotation

`KEK_BASE64` is the AES-GCM key that wraps every encrypted column at
rest: `upstream_credentials.pat_enc`, `upstream_credentials.issuer_secret_enc`,
`oidc_providers.client_secret_enc`, and `root_keys.private_key_enc`.
It is the single most important secret in the system.

**What happens if KEK is lost:**

> **Every encrypted column at rest becomes unrecoverable.** The
> gateway cannot decrypt any upstream credential, so no OCI tokens
> can be minted and no Releases downloads can be proxied. Customer
> tokens stop working. OIDC client secrets and any private root keys
> are also unreadable. Operationally, recovery is *re-enter every
> upstream credential and OIDC client secret*. There is no backup
> story for the KEK itself — back it up out-of-band (e.g. password
> manager, KMS) the moment you generate it.

**Rotation playbook (manual, v1):**

1. Generate a new KEK: `openssl rand -base64 32`.
2. Bring the gateway down (`kubectl scale --replicas=0`).
3. Run a one-off re-encrypt job: `artifact-gateway --rekey
   --old-kek=$OLD --new-kek=$NEW` (not yet implemented — tracked
   separately; until then KEK is effectively immutable).
4. Update the Secret, scale back up.

`SESSION_SIGNING_KEY` and `JWT_SIGNING_KEY` can be rotated by simply
replacing them and restarting; the only effect is that existing
sessions log out and outstanding OCI bearer JWTs become invalid
(clients re-mint within seconds).

`SERVICE_TOKEN` and `ADMIN_BOOTSTRAP_PASSWORD` are similarly safe to
rotate — they are not used to encrypt anything at rest.

## Cert / hostname constraint  ⚠️ **read this before you deploy**

`EXTERNAL_HOSTNAME` must equal the hostname customers type into
`docker login`, AND that hostname must appear in the SAN list of the
TLS certificate terminating the ingress.

If they differ, OCI clients fail with opaque errors like:

```
Error response from daemon: Get "https://artifacts.example.com/v2/":
  x509: certificate is valid for *.example.com, not artifacts.example.com
```

There is no graceful degradation. `docker`/`helm`/`oras` simply
refuse. This is the single most common deployment misconfiguration —
the chart documents it loudly in `NOTES.txt`, the ingress template,
and this file.

**Practical rules:**

- One `EXTERNAL_HOSTNAME` per gateway install.
- If you front the gateway with multiple hostnames (CDN, vanity URL,
  internal alias), the cert must include **all of them** in its SAN
  list. Only the canonical one goes in `EXTERNAL_HOSTNAME`.
- Wildcard certs (`*.example.com`) work — but only one label deep.
  `artifacts.acme.example.com` is **not** covered by `*.example.com`.

## Day-2 operations

### Backup

The only stateful component is Postgres. Back it up with whatever you
use for the rest of your Postgres fleet (`pg_dump`, CNPG backups, RDS
snapshots). Nothing on the gateway pod is persistent —
`readOnlyRootFilesystem: true`. There is no PVC.

Back up the KEK separately and *out of band*. A database dump
without the KEK is a brick.

### Upgrade

The gateway creates its schema on startup against an empty database
(idempotent `CREATE TABLE IF NOT EXISTS` + `ON CONFLICT DO NOTHING`).
There is no separate migration phase; upgrades are a normal Helm rollout.

Standard upgrade flow:

```
helm upgrade artifact-gateway . -f values.yaml -f values-prod.yaml
```

The chart remains single-replica by default. Rate-limit accounting
for the download flow is per-replica until a shared (e.g. NATS KV)
counter lands, so do not enable HPA without that work.

### Key rotation (playbook stub)

| Key                        | Rotation impact                  | Steps |
| -------------------------- | -------------------------------- | ----- |
| `SESSION_SIGNING_KEY`      | All admin/catalog sessions drop  | edit Secret → `kubectl rollout restart deploy/...` |
| `JWT_SIGNING_KEY`          | Outstanding OCI tokens invalid (~5 min impact) | edit Secret → rollout restart |
| `SERVICE_TOKEN`            | Any internal caller using old token rejected | edit Secret → rollout restart, then update callers |
| `ADMIN_BOOTSTRAP_PASSWORD` | None at runtime (used only when no users exist) | edit Secret freely |
| **`KEK_BASE64`**           | **Do not rotate without `--rekey` (not in v1).** Replacing the KEK without re-encrypting orphans every encrypted column at rest. | (see KEK Rotation Playbook above) |

### Observability

- `/health/live` — process is up. Used by liveness probe.
- `/health/ready` — DB pool healthy, ready to serve. Used by readiness probe.
- `/metrics` on the management port — Prometheus, namespace
  `artifact_gateway`. Scrape via the chart's `ServiceMonitor` when
  `metrics.serviceMonitor.enabled=true`.

Key SLO metrics to alert on:

- `token_mint_latency_seconds` p99 — token mint should be < 100 ms.
- `upstream_errors_total` — non-zero means ghcr is unreachable or the
  PAT is invalid.
- `license_check_failures_total{reason="expired"}` — expected when a
  customer's license lapses; spikes indicate a clock or NATS-cache
  issue.

### Customer-token single-active enforcement (one-time)

The `customer_tokens` table now has a partial unique index that allows at
most one active (non-revoked) row per `license_id`. Fresh databases create
the index on first boot with no action required.

For databases that pre-date this constraint and contain licenses with `>1`
active tokens, the server refuses to start until an operator explicitly
acknowledges the cleanup:

1. Start the new image. The process logs a report listing each
   `(license_id, active_count)` tuple and prints the expected ACK value:
   ```
   customer_tokens single-active reconcile required
     expected_ack=<sha256>
     hint=set CUSTOMER_TOKENS_ACK_REVOKE=<sha256> and restart
   ```
2. Notify affected customers that older credentials will be revoked. The
   reconciler keeps the **newest** active token per license (by
   `created_at`) and sets `revoked_at = now()` on the rest.
3. Restart with `CUSTOMER_TOKENS_ACK_REVOKE=<sha256>` set (e.g. via the
   chart's `env` block on the Deployment). On boot the reconciler runs in
   a single transaction, then creates the partial unique index. The ACK
   value is tied to the exact `(license_id, count)` set so it cannot be
   reused if the set of offenders changes between boots.
4. Subsequent restarts no-op — the report query returns zero rows.

From that point onward both customer-facing self-service rotation
(`POST /catalog/api/credential/rotate`) and admin rotation
(`POST /api/v1/customer-tokens/rotate`) atomically revoke any prior
active row and insert the new one. The legacy `POST /api/v1/customer-tokens`
endpoint still works but routes through the rotate path and returns a
`Deprecation: true` header.
