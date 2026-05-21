# artifact-gateway — Deployment Story

This document is the canonical reference for shipping and operating
`artifact-gateway`. It complements (and never contradicts) the
implementation plan at
`/Users/wcrum/.claude/plans/linked-exploring-sutton.md`.

## Surfaces we ship

| Surface                         | Ship?       | Rationale |
| ------------------------------- | ----------- | --------- |
| Docker image (multi-stage)      | **Yes**     | Primary deployment artifact. Distroless, ~30 MB, embedded UI. Published to `ghcr.io/cnak-us/artifact-gateway`. |
| Helm chart (OCI)                | **Yes**     | Kubernetes is the production target. Chart is published as an OCI artifact to `ghcr.io/cnak-us/charts/artifact-gateway` so it can be `helm install` and `helm pull oci://`. |
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

Cross-referenced against `config/config.go`. Every var the binary reads.

| Var                       | Default                   | Source            | Secret? | Notes |
| ------------------------- | ------------------------- | ----------------- | ------- | ----- |
| `PUBLIC_PORT`             | `8080`                    | ConfigMap         | no      | OCI + admin + catalog listener |
| `MANAGEMENT_PORT`         | `8090`                    | ConfigMap         | no      | `/health/*` + `/metrics` |
| `EXTERNAL_HOSTNAME`       | `localhost:8080`          | ConfigMap         | no      | **MUST match the TLS cert SAN exactly** — see Cert/hostname constraint |
| `UPSTREAM_GITHUB_API`     | `https://api.github.com`  | ConfigMap         | no      | GitHub REST API for non-OCI download sources (releases). Override to a GHES URL like `https://ghes.example.com/api/v3` |
| `TOKEN_TTL_SECONDS`       | `300`                     | ConfigMap         | no      | OCI bearer JWT lifetime |
| `OIDC_AUTOPROVISION`      | `false`                   | ConfigMap         | no      | When true, new OIDC users land as `role='viewer'` |
| `LOG_LEVEL`               | `info`                    | ConfigMap         | no      | `debug|info|warn|error` |
| `LOG_FORMAT`              | `json`                    | ConfigMap         | no      | `json|text` |
| `NATS_URL`                | (empty)                   | ConfigMap         | no      | Empty disables NATS publish + license cache invalidation |
| `NATS_CREDENTIALS_FILE`   | (empty)                   | Mounted file path | no      | Path to a `.creds` file mounted from `nats.credentialsSecret` |
| `NATS_AUTH_TOKEN`         | (empty)                   | Secret            | **yes** | Only used when NATS is password-authed |
| `POD_NAME`                | (empty)                   | Downward API      | no      | For HA cache key disambiguation |
| `DATABASE_URL`            | (empty)                   | Secret            | **yes** | `postgres://user:pass@host:5432/db?sslmode=require` |
| `KEK_BASE64`              | (empty)                   | Secret            | **yes** | 32 random bytes, base64. **AES-GCM KEK for stored ghcr PATs.** Losing this bricks everything at rest. |
| `SESSION_SIGNING_KEY`     | (empty)                   | Secret            | **yes** | Hex, ≥ 32 bytes. HMAC-SHA256 for admin/catalog cookies |
| `JWT_SIGNING_KEY`         | (empty)                   | Secret            | **yes** | Hex, ≥ 32 bytes. HMAC-SHA256 for OCI bearer JWTs |
| `SERVICE_TOKEN`           | (empty)                   | Secret            | **yes** | Shared secret for any internal service callers |
| `ADMIN_BOOTSTRAP_EMAIL`   | (empty)                   | Secret            | **yes** | One-time. Used only if no users exist at startup |
| `ADMIN_BOOTSTRAP_PASSWORD`| (empty)                   | Secret            | **yes** | One-time. Rotate after first login. |

The chart enforces "secret → Secret, non-secret → ConfigMap" with
`envFrom:` on both. There is no inline `env:` other than `POD_NAME`
from the downward API.

## Upstream credentials: required PAT scopes

Operators add one upstream credential per registry via the admin UI or
the config-apply manifest. The required token scope depends on the
credential `kind`:

| Kind         | Required scope / permission                                                                                       |
|--------------|-------------------------------------------------------------------------------------------------------------------|
| `ghcr`       | Classic PAT with `read:packages`. GHCR does not support fine-grained PATs for `docker pull`.                       |
| `github-api` | Classic PAT with `repo` (private) or `public_repo` (public). Fine-grained: Contents=Read, Metadata=Read.           |
| `oci-basic`  | Pull/read on the target repository. Gitea: `read:package`. Harbor: robot account with pull+read. Artifactory: Identity Token with repo read. ACR scope-mapped tokens also fit here. Self-hosted instances can paste an internal CA chain into `ca_bundle_pem`. |
| `dockerhub`  | Docker Hub PAT with Read scope (`hub.docker.com → Account Settings → Security`). Host is pinned to `registry-1.docker.io`. |
| `quay`       | Robot account name (`org+robotname`) and robot token with read permission on the target repos. Defaults to `quay.io`; set `base_url` for self-hosted Quay. |
| `gitlab`     | Project/Group Deploy Token (recommended) or PAT with `read_registry`. Set `base_url` to the registry host (`registry.gitlab.com` for SaaS, `registry.<your-domain>` for self-hosted). The proxy auto-discovers the JWT realm from the upstream's 401 challenge. |
| `ecr`        | IAM principal with `ecr:GetAuthorizationToken`, `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer`. Provide `issuer_secret={accessKeyId, secretAccessKey}` and `issuer_config={region, accountId}`. The proxy mints a 12-hour Basic credential and refreshes before expiry. |
| `gar`        | Google service-account JSON with `roles/artifactregistry.reader`. Provide `issuer_secret` (raw SA JSON) and `base_url` (the regional registry, e.g. `https://us-docker.pkg.dev`). The proxy mints a ~1h OAuth2 access token and refreshes before expiry. |
| `acr-aad`    | Azure service principal with AcrPull. Provide `issuer_secret={clientId, clientSecret}`, `issuer_config={tenantId, registry}`, and `base_url=https://<name>.azurecr.io`. The proxy does AAD → ACR refresh → ACR access exchange and refreshes the short-lived (~5 min) access token. |

## Secret model and KEK rotation

`KEK_BASE64` is a Key Encryption Key, not a Data Encryption Key. It
wraps the per-row AES-GCM nonces for `upstream_credentials.pat_enc`
(ghcr PATs). It is the single most important secret in the system.

**What happens if KEK is lost:**

> **Every encrypted column at rest becomes unrecoverable.** The
> `upstream_credentials` table is unreadable; you cannot mint OCI
> tokens for any package because the gateway cannot decrypt the
> upstream PAT. Customer tokens stop working. Operationally, recovery
> is *re-enter every ghcr PAT*. There is no backup story for the KEK
> itself — back it up out-of-band (e.g. password manager, KMS) the
> moment you generate it.

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
| **`KEK_BASE64`**           | **Do not rotate without `--rekey` (not in v1).** Replacing the KEK without re-encrypting orphans every stored ghcr PAT. | (see KEK Rotation Playbook above) |

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
