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

The chart auto-generates the KEK and signing keys on first install and
preserves them across upgrades via `lookup`. The env Secret is annotated
`helm.sh/resource-policy: keep` so `helm uninstall` does NOT delete it
(losing `KEK_BASE64` orphans every encrypted column at rest). Back the
Secret up out of band anyway — the resource policy doesn't protect against
namespace deletion or accidental `kubectl delete`.

## Install (production-shape)

```bash
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
  --set bootstrapAdmin.email=admin@example.com \
  --set-string bootstrapAdmin.password='change-me-now'
```

Or use the structured `externalDatabase` block to share the Postgres
connection with the bundled Dex IdP:

```bash
helm install artifact-gateway . \
  --namespace artifact-gateway --create-namespace \
  --set externalDatabase.enabled=true \
  --set externalDatabase.host=postgres.example.com \
  --set externalDatabase.user=cnak \
  --set externalDatabase.password=$DB_PASSWORD \
  --set dex.enabled=true \
  --set dex.configSecret.create=false \
  --set dex.configSecret.name=artifact-gateway-dex-config \
  --set config.externalHostname=artifacts.example.com
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
helm install artifact-gateway oci://ghcr.io/cnak-us/artifact-gateway/chart \
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

The gateway initialises its schema on startup, so no separate migration
hook runs.

## Uninstall

```bash
helm uninstall artifact-gateway --namespace artifact-gateway
```

Postgres data is **not** removed — that lives in your DB. The env Secret
(KEK / signing keys / service token) is annotated
`helm.sh/resource-policy: keep`, so `helm uninstall` keeps it. Reinstalling
with the same release name will reuse those keys; if you delete the Secret
manually, encrypted columns at rest become unreadable.

## Values reference

See [`values.yaml`](values.yaml) for the full annotated reference.
