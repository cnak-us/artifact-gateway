# artifact-gateway — SSO via Dex

Dex is the **default** identity provider for local development. Running `make dev` starts Dex automatically — no extra flags or manual provider registration needed.

```
browser ──▶ gateway (localhost:8080) ──OIDC──▶ Dex (localhost:5556) ──OAuth──▶ github.com
```

---

## 1. Why Dex

GitHub, GitLab, and most SAML / LDAP directories are **not** OIDC-compliant — they don't issue ID tokens, and their OAuth dialects each have their own quirks. The gateway, by design, only speaks OIDC. Bridging the two in gateway code would mean a new connector per IdP and a release every time an upstream changes its scopes.

Dex sits between the two: it speaks real OIDC to the gateway and the upstream's native protocol (OAuth, SAML, LDAP) to the IdP. The gateway sees exactly **one** OIDC provider — `dex` — auto-provisioned in the database on startup. Operators add new IdPs by appending entries to `config/dex.dev.yaml` under `connectors:`. No gateway code changes.

---

## 2. The redirect-URI dance

Two URIs that an operator must keep straight. One gets pasted into the upstream console; the other is automatic.

| Where | What goes there |
|---|---|
| **GitHub OAuth App** → Authorization callback URL | `http://localhost:5556/callback` |
| **Dex config** → `staticClients[].redirectURIs` | gateway's `/api/v1/auth/oidc/dex/callback` (already in `config/dex.dev.yaml`) |

The chain on a sign-in: browser hits the gateway, the gateway 302s to Dex, Dex 302s to `github.com`, GitHub redirects back to **Dex** at `http://localhost:5556/callback`, Dex mints an ID token and redirects back to **the gateway** at `http://localhost:8080/api/v1/auth/oidc/dex/callback`. Each hop's redirect lands at the URI registered in that hop's config — get one wrong and the chain breaks at that step with a `redirect_uri_mismatch`.

The catalog URL `http://localhost:8080/catalog/oidc/dex/callback` is still present in the static client config for back-compat but is no longer reached in normal flows.

---

## 3. One-time setup

### 1. Create a GitHub OAuth App

Go to https://github.com/settings/developers → **New OAuth App**.

- **Application name**: anything (e.g. `artifact-gateway dev`).
- **Homepage URL**: `http://localhost:8080`
- **Authorization callback URL**: `http://localhost:5556/callback`

That callback URL is Dex's, **not** the gateway's. This is the single most common mistake. Save the app, then **Generate a new client secret**.

### 2. Paste credentials into `.env`

```
GITHUB_CLIENT_ID=Iv1.xxxxxxxxxxxxxxxx
GITHUB_CLIENT_SECRET=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Re-running `make dev-init` is **not** needed for this — it only backfills missing random secrets and Dex defaults, and won't touch values you've set by hand.

### 3. Start the stack

```bash
make dev
```

This runs `make dev-init` (idempotent), then brings up postgres + registry + Dex, waits for each to be healthy, and runs the gateway on plain HTTP. The gateway reads `DEX_ISSUER_URL`, `DEX_CLIENT_ID`, and `DEX_CLIENT_SECRET` from the environment and automatically creates (or updates) the `dex` OIDC provider row in the database — **no admin UI registration step needed**.

Wait for the line:

```
running artifact-gateway
```

### 4. Sanity check Dex

```bash
curl http://localhost:5556/.well-known/openid-configuration | jq .issuer
```

Should print:

```
"http://localhost:5556"
```

If you get connection refused, Dex didn't come up — check `docker compose logs dex`.

### 5. Sign in via Dex

From the admin login page, click **Sign in with dex**. You'll be redirected to Dex's connector chooser, pick **GitHub**, authorize on GitHub, and land back on the admin dashboard.

---

## Skipping Dex (break-glass)

If Dex is unavailable or you need to log in with the local bootstrap credentials, bypass the auto-redirect by visiting:

```
http://localhost:8080/login?manual=1
```

Then navigate to `/admin/login` to sign in with email/password directly.

---

## 4. Adding more IdPs

Each new upstream IdP is a single new entry in `config/dex.dev.yaml` under `connectors:`. The gateway side stays untouched — same provider `dex`, same client id, same client secret. Restart Dex (`docker compose up -d dex`) to pick up the new connector.

**Google** (already OIDC-native, but routing it through Dex keeps the operator UX uniform):

```yaml
- type: oidc
  id: google
  name: Google
  config:
    issuer: https://accounts.google.com
    clientID: $GOOGLE_CLIENT_ID
    clientSecret: $GOOGLE_CLIENT_SECRET
    redirectURI: http://localhost:5556/callback
```

**GitLab** (OAuth, like GitHub):

```yaml
- type: gitlab
  id: gitlab
  name: GitLab
  config:
    clientID: $GITLAB_CLIENT_ID
    clientSecret: $GITLAB_CLIENT_SECRET
    redirectURI: http://localhost:5556/callback
```

Each new connector needs its own OAuth/SAML app on the upstream, and that upstream's callback URL is **always** `http://localhost:5556/callback` — Dex multiplexes by connector `id`, not by callback path.

---

## 5. Common failure modes

- **`redirect_uri_mismatch` from GitHub** → your OAuth App's callback URL doesn't exactly equal `http://localhost:5556/callback`. Trailing slashes count.
- **`redirect_uri_mismatch` from Dex** (returned to the gateway) → the gateway's callback isn't in `staticClients[].redirectURIs`. The default config covers both `http://` and `https://` localhost:8080; if you change the public port or hostname, add the matching variant.
- **`provider returned no id_token and userinfo lookup failed`** → you registered `github.com` directly as the OIDC provider instead of Dex. GitHub doesn't issue ID tokens. Fix: point the issuer at `http://localhost:5556`.
- **`cannot validate token: signed by unknown key`** → Dex restarted with `storage.type: memory` and rotated its signing keys. Restart the gateway or wait for the JWKS cache to refresh.
- **Login succeeds but you land at `/admin` and immediately bounce back to the login page** → `OIDC_AUTOPROVISION=false` (default) and no user row exists for that email yet. Either flip the env var to `true` (auto-provisions as `role=viewer`; an admin must promote) or insert the user row manually before signing in.
- **Gateway can't reach `http://localhost:5556` inside Docker** → the `extra_hosts: ["localhost:host-gateway"]` entry in `docker-compose.yml` maps `localhost` to the host machine's IP. If your Docker version doesn't support `host-gateway`, replace it with `host.docker.internal` or use `network_mode: host` on the gateway service.

---

## 6. Theme

The Dex login pages use a custom theme at `dex/web/themes/artifact-gateway/` that matches the artifact-gateway SPA's dark design system.

- **`styles.css`** — all visual styles (CSS variables, card layout, connector buttons, form inputs, alerts). Edit this to change colors or component appearance.
- **`logo.svg`** — the product logo SVG (hexagon + crosshair, accent blue `#6e9fff`).
- **`favicon.png`** — browser tab icon.
- **`dex/web/templates/`** — Dex template overrides (header, footer, login, password, approval, device, error, oob). Edit copy here; keep all `{{ ... }}` template variables intact or Dex will fail to render.

The theme is mounted into the Dex container at `/srv/dex/web` via `docker-compose.yml` and activated by the `frontend:` block in `config/dex.dev.yaml`.

---

## 7. Production notes

The dev config is intentionally not production-safe. Before exposing Dex to anything real:

- **Swap `storage.type: memory` for `postgres`.** Memory storage loses sessions and signing keys on every restart, which invalidates every issued token.
- **Pin the issuer to your real hostname** (e.g. `https://dex.example.com`). The issuer URL is part of the OIDC contract — changing it after tokens are in the wild invalidates them. Update `OIDC_DEFAULT_PROVIDER` (and the corresponding `DEX_ISSUER_URL` env var on the gateway pod) to match.
- **Terminate TLS at the load balancer.** Don't expose Dex's HTTP listener directly to the internet.
- **Rotate the static client secret.** `dev-dex-client-secret` is intentionally weak and well-known so it can never be confused with a real production credential. Set `DEX_CLIENT_SECRET` to a strong random value and update the `staticClients[].secret` field in the Dex config to match.
- **Update each connector's upstream callback URL** to match the production Dex hostname (e.g. `https://dex.example.com/callback`) in every upstream OAuth/SAML app's console.
- **`OIDC_DEFAULT_PROVIDER`** controls which registered provider the gateway redirects unauthenticated admin requests to. Set it to `dex` (or whatever slug you used when registering) on the gateway pod's environment.
