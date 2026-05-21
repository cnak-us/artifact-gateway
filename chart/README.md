# artifact-gateway Helm chart

Helm chart for `artifact-gateway` — a Kubernetes-native OCI auth gateway
that proxies `ghcr.io` and gates customer access by cnak license.

For the deployment design and operational story (KEK rotation, env var
matrix, day-2 ops), read [`../DEPLOYMENT.md`](../DEPLOYMENT.md).

## Prerequisites

- Kubernetes ≥ 1.24
- Helm ≥ 3.8 (for OCI support)
- A reachable Postgres instance (this chart does NOT bundle a database)
- An ingress controller and a TLS certificate covering
  `config.externalHostname`. **The cert SAN must exactly match the
  hostname customers type into `docker login`** — see the warning in
  `templates/ingress.yaml`. `cert-manager` is recommended.
- A 32-byte KEK (`openssl rand -base64 32`) — **back this up out of
  band, losing it bricks every encrypted column at rest.**

## Install (production-shape)

```bash
KEK=$(openssl rand -base64 32)
SESSION=$(openssl rand -hex 32)
JWT=$(openssl rand -hex 32)
SVC=$(openssl rand -hex 16)

helm install artifact-gateway . \
  --namespace artifact-gateway --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=artifacts.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix \
  --set ingress.tls[0].secretName=artifact-gateway-tls \
  --set ingress.tls[0].hosts[0]=artifacts.example.com \
  --set config.externalHostname=artifacts.example.com \
  --set-string database.url='postgres://user:pass@postgres:5432/artifact_gateway?sslmode=require' \
  --set-string secrets.kekBase64="$KEK" \
  --set-string secrets.sessionSigningKey="$SESSION" \
  --set-string secrets.jwtSigningKey="$JWT" \
  --set-string secrets.serviceToken="$SVC" \
  --set bootstrapAdmin.email=admin@example.com \
  --set-string bootstrapAdmin.password='change-me-now'
```

## Install (local dev)

```bash
helm install artifact-gateway . -f values-dev.yaml
```

`values-dev.yaml` is intentionally permissive — fixed dev secrets, no
ingress, `localhost:5000` upstream. Never use it outside of kind/minikube.

## Install from OCI

When the chart is published as an OCI artifact:

```bash
helm install artifact-gateway oci://ghcr.io/cnak-us/charts/artifact-gateway \
  --version 0.1.0 \
  --values your-values.yaml
```

## Managing the env Secret yourself

For production, manage the Secret out of band (`SealedSecret`, External
Secrets, Vault) and point the chart at it:

```yaml
secrets:
  create: false
  existingSecret: artifact-gateway-env

database:
  existingSecret: artifact-gateway-env
  existingSecretKey: DATABASE_URL
```

The secret must contain at minimum:

| Key                       | Required |
| ------------------------- | -------- |
| `KEK_BASE64`              | yes      |
| `SESSION_SIGNING_KEY`     | yes      |
| `JWT_SIGNING_KEY`         | yes      |
| `SERVICE_TOKEN`           | yes      |
| `DATABASE_URL`            | yes (when `database.existingSecret` is the same secret) |
| `ADMIN_BOOTSTRAP_EMAIL`   | optional, one-time |
| `ADMIN_BOOTSTRAP_PASSWORD`| optional, one-time |
| `NATS_AUTH_TOKEN`         | optional |

## Upgrade

```bash
helm upgrade artifact-gateway . -f values.yaml -f values-prod.yaml
```

A `pre-install,pre-upgrade` Hook Job runs the migrations (image with
`MIGRATE_ONLY=true`). If the migration fails, the upgrade aborts before
the Deployment is touched.

## Uninstall

```bash
helm uninstall artifact-gateway --namespace artifact-gateway
```

Postgres data is **not** removed — that lives in your DB. The KEK is
gone unless you backed it up.

## Values reference

See [`values.yaml`](values.yaml) for the full annotated reference.
