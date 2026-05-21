# artifact-gateway

Kubernetes-native OCI auth gateway. Proxies `ghcr.io` for license-gated customer access without exposing the upstream PAT.

## What it does

- Admins register packages (containers, helm charts, binaries-as-OCI) and an upstream `ghcr` credential
- Admins generate per-customer credentials bound to a `cnaklic` license
- Customers use ordinary `docker pull`, `helm pull oci://`, `oras pull` — the gateway re-checks the license on every token mint and proxies to ghcr server-side
- Single Go binary, embedded React UI, Postgres backing store

## Endpoints

- `:8080` — public
  - `/v2/*` — OCI Distribution Spec v2
  - `/admin/*` — admin UI (React SPA)
  - `/catalog/*` — customer catalog UI
  - `/api/v1/*` — admin REST API
- `:8090` — management
  - `/health/live`, `/health/ready`, `/metrics`

## Quick start (local dev)

```bash
make dev-init   # one-time: generate .env with random secrets
make dev        # brings up postgres + registry, then runs `go run .`
```

`make dev` starts Postgres (port 5432) and a CNCF `registry:2` (port 5000) via Docker Compose, waits for Postgres to be healthy, then runs the gateway on the host with the secrets in `.env` loaded into the environment.

Stop the dependencies with `make dev-stop`. To wipe the database volume as well, use `make compose-down`.

### Other useful targets

```bash
make help        # list everything
make build       # compile ./bin/artifact-gateway
make build-ui    # build the React UI into ui/dist (consumed by go:embed)
make test        # go test ./...
make lint        # go vet + golangci-lint (if installed)
make migrate     # run goose migrations and exit
make image       # docker build (context is parent dir; see below)
make smoke       # end-to-end docker/helm/oras pull test (needs TID and SECRET)
```

### Docker image

The image is built from the repo root (one level above this directory) because `go.mod` has `replace github.com/cnak-us/cnak/pkg => ../cnak/pkg`. The Compose `build` block and the `image` Make target already handle this:

```bash
# Via make (recommended)
make image

# Manually
docker build -f artifact-gateway/Dockerfile -t artifact-gateway:dev ..
```

### Full stack in Docker

```bash
make compose-up      # postgres + registry + artifact-gateway
make compose-down    # stop and DELETE volumes
```

By default this pulls `ghcr.io/cnak-us/artifact-gateway:dev`. To build from source, uncomment the `build:` block in `docker-compose.yml` or run `docker compose --profile build up -d --build`.

To also bring up NATS (optional — used for audit fanout and license-cache invalidation):

```bash
docker compose --profile nats up -d
```

## Configuration

All configuration is via environment variables. See [`.env.example`](.env.example) for the full list with comments. The most important ones:

| Variable                    | Purpose                                                                                |
|-----------------------------|----------------------------------------------------------------------------------------|
| `PUBLIC_PORT`               | OCI + UI + admin REST listener (default `8080`)                                        |
| `MANAGEMENT_PORT`           | Health probes + Prometheus `/metrics` (default `8090`)                                 |
| `EXTERNAL_HOSTNAME`         | Hostname customers use in `docker login` — **must match cert SAN in production**       |
| `DATABASE_URL`              | Postgres connection string                                                             |
| `KEK_BASE64`                | 32 random bytes (base64) — AES-GCM key for stored ghcr PATs                            |
| `SESSION_SIGNING_KEY`       | 32 random bytes (hex) — HMAC for admin/catalog signed cookies                          |
| `JWT_SIGNING_KEY`           | 32 random bytes (hex) — HMAC for OCI bearer JWTs                                       |
| `TOKEN_TTL_SECONDS`         | OCI bearer JWT lifetime (default `300`)                                                |
| `ADMIN_BOOTSTRAP_EMAIL`     | First admin account, created on first startup if no users exist                        |
| `ADMIN_BOOTSTRAP_PASSWORD`  | First admin password — change it after first login                                     |
| `NATS_URL`                  | Optional — leave blank to disable audit fanout and license-cache invalidation          |
| `MIGRATE_ONLY`              | If `true`, run migrations and exit (used by the chart's pre-install Job)               |

### Upstream credentials

Admins register one credential per upstream registry. The credential `kind` selects how the proxy authenticates against the upstream:

| Kind         | What it pulls                          | Required scope / permission                                                                                  | Extra fields                            |
|--------------|----------------------------------------|--------------------------------------------------------------------------------------------------------------|------------------------------------------|
| `ghcr`       | OCI manifests/blobs from `ghcr.io`     | Classic PAT with `read:packages`. GHCR does not support fine-grained PATs for `docker pull`.                 | —                                        |
| `github-api` | GitHub Releases asset downloads        | Classic PAT with `repo` (private) or `public_repo` (public). Fine-grained: Contents=Read, Metadata=Read.     | —                                        |
| `oci-basic`  | Any Basic-auth OCI registry            | Token with pull/read on the target repository. Gitea: `read:package`. Harbor: robot account with pull+read. Artifactory: Identity Token with repo read. ACR scope-mapped tokens also fit here. | `base_url` (required); optional `ca_bundle_pem`, `insecure_skip_tls_verify` |
| `dockerhub`  | Docker Hub                             | Docker Hub PAT with Read scope. Host pinned to `registry-1.docker.io`.                                       | —                                        |
| `quay`       | Quay.io (or self-hosted)               | Robot account name + token with `read` on target repos.                                                      | `base_url` (optional; default `https://quay.io`) |
| `gitlab`     | GitLab Container Registry              | Deploy Token (preferred) or PAT with `read_registry`.                                                        | `base_url` (required; e.g. `https://registry.gitlab.com`) |
| `ecr`        | AWS Elastic Container Registry         | IAM principal with `ecr:GetAuthorizationToken`, `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer`.            | `issuer_secret` JSON `{accessKeyId, secretAccessKey}`, `issuer_config` `{region, accountId}` |
| `gar`        | Google Artifact Registry / GCR         | Service account with `roles/artifactregistry.reader` (or `storage.objectViewer` on legacy GCR backing bucket). | `issuer_secret` (raw SA JSON), `base_url` (e.g. `https://us-docker.pkg.dev`) |
| `acr-aad`    | Azure Container Registry via AAD       | Service principal with AcrPull on the registry.                                                              | `issuer_secret` `{clientId, clientSecret}`, `issuer_config` `{tenantId, registry}`, `base_url` |

`oci-basic` is the catch-all for any registry that accepts a static PAT directly on `/v2/*` (Gitea, Forgejo, Harbor, JFrog Artifactory, ACR scope-mapped tokens, Zot, distribution/distribution). The `dockerhub`/`quay`/`gitlab` kinds layer a Docker token-exchange (`401 → realm → bearer`) handshake on top — the proxy mints a scope-pinned JWT on demand and caches it per credential. The `ecr`/`gar`/`acr-aad` kinds mint short-lived registry tokens from a stored cloud issuer credential and refresh in the background before expiry. For self-hosted instances behind an internal CA, paste the cert chain into `ca_bundle_pem`.

## Production deployment

A production-ready Helm chart ships in [`chart/`](chart/) and the full deployment guide lives in [`DEPLOYMENT.md`](DEPLOYMENT.md). In short:

```bash
helm upgrade --install artifact-gateway ./chart \
  --namespace artifact-gateway --create-namespace \
  -f my-values.yaml
```

The chart expects an external Postgres (CloudNativePG or Bitnami `postgresql` chart both work) and a `Secret` containing `DATABASE_URL`, `KEK_BASE64`, `SESSION_SIGNING_KEY`, `JWT_SIGNING_KEY`, and `SERVICE_TOKEN`. Ingress + cert-manager annotations are commented in `values.yaml`.

The published image is `ghcr.io/cnak-us/artifact-gateway`. Tags:

- `latest` — most recent release
- `vX.Y.Z` — release tag
- `dev` — main-branch build
- `sha-<commit>` — pinned build for reproducible deploys

## CI/CD

GitHub Actions workflows live in [`.github/workflows/`](.github/workflows/):

- **build.yml** — go test + lint + image build on every push/PR
- **release.yml** — multi-arch image push to `ghcr.io/cnak-us/artifact-gateway` on tag

## License

Apache-2.0
