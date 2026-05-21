# artifact-gateway — local-dev Makefile.
#
# Run `make` (or `make help`) for the list of available targets.
# Most developers only need:
#   make dev-init    # one-time: generate .env with random secrets
#   make dev         # bring up postgres + registry, then `go run .`

SHELL          := /bin/bash
.SHELLFLAGS    := -eu -o pipefail -c

# Image coordinates. The chart and CI workflows must use the same name.
IMAGE          ?= ghcr.io/cnak-us/artifact-gateway
IMAGE_TAG      ?= dev
VERSION        ?= dev

# Self-contained build: context is the repo root.
DOCKER_CONTEXT ?= .
DOCKERFILE     ?= Dockerfile

UI_SRC         := ui/src
UI_DIST        := ui/dist
BIN_DIR        := bin
BIN            := $(BIN_DIR)/artifact-gateway

ENV_FILE       := .env
ENV_EXAMPLE    := .env.example

# mkcert-managed dev TLS material. Mirrors how the chart mounts a Kubernetes
# TLS Secret — same env-var contract (TLS_CERT_FILE / TLS_KEY_FILE), just
# pointed at on-disk PEMs instead of /var/run/secrets/...
CERT_DIR       := certs
DEV_CERT       := $(CERT_DIR)/dev.pem
DEV_KEY        := $(CERT_DIR)/dev-key.pem
DEV_CERT_HOSTS ?= localhost 127.0.0.1 ::1

# Helm release coordinates. Override on the command line, e.g.
#   make helm-upgrade HELM_VALUES="-f chart/values-do.yaml" HELM_ARGS="--set image.tag=v1.2.3"
HELM_RELEASE   ?= artifact-gateway
HELM_NAMESPACE ?= artifact-gateway
HELM_CHART     ?= ./chart
HELM_VALUES    ?= -f $(HELM_CHART)/values-dev.yaml
HELM_ARGS      ?=

.DEFAULT_GOAL  := help
.PHONY: help dev-init dev dev-https dev-dex dev-certs dev-certs-clean dev-stop \
        build build-ui test lint image \
        compose-up compose-down smoke clean \
        helm-install helm-upgrade helm-uninstall

## help: print available targets
help:
	@awk 'BEGIN{FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*?##/ {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## dev-init: ensure .env exists and has non-empty secrets (creates from .env.example
##           if missing; back-fills empty KEK_BASE64 / SESSION_SIGNING_KEY / JWT_SIGNING_KEY
##           in-place if they're blank — re-run any time the secrets are wiped).
dev-init: ## create or back-fill .env with random secrets
	@if [ ! -f "$(ENV_EXAMPLE)" ]; then \
		echo "ERROR: $(ENV_EXAMPLE) is missing"; exit 1; \
	fi
	@if ! command -v openssl >/dev/null 2>&1; then \
		echo "ERROR: openssl is required to generate secrets"; exit 1; \
	fi
	@if [ ! -f "$(ENV_FILE)" ]; then \
		cp "$(ENV_EXAMPLE)" "$(ENV_FILE)"; \
		echo ">>> created $(ENV_FILE) from $(ENV_EXAMPLE)"; \
	fi
	@KEK=$$(openssl rand -base64 32); \
	SESSION=$$(openssl rand -hex 32); \
	JWT=$$(openssl rand -hex 32); \
	awk -v kek="$$KEK" -v sess="$$SESSION" -v jwt="$$JWT" ' \
		/^KEK_BASE64=[[:space:]]*$$/        {print "KEK_BASE64=" kek; changed=1; next} \
		/^SESSION_SIGNING_KEY=[[:space:]]*$$/ {print "SESSION_SIGNING_KEY=" sess; changed=1; next} \
		/^JWT_SIGNING_KEY=[[:space:]]*$$/   {print "JWT_SIGNING_KEY=" jwt; changed=1; next} \
		{print} \
		END {exit changed ? 0 : 99}' "$(ENV_FILE)" > "$(ENV_FILE).tmp"; \
	rc=$$?; \
	if [ "$$rc" = "0" ]; then \
		mv "$(ENV_FILE).tmp" "$(ENV_FILE)"; \
		echo ">>> back-filled empty secrets in $(ENV_FILE)"; \
	elif [ "$$rc" = "99" ]; then \
		rm -f "$(ENV_FILE).tmp"; \
		echo ">>> $(ENV_FILE) secrets already populated — leaving them alone"; \
	else \
		rm -f "$(ENV_FILE).tmp"; \
		echo "ERROR: awk failed with rc=$$rc"; exit 1; \
	fi
	@# Write Dex defaults if not already present (idempotent — won't overwrite existing values).
	@# DEX_ISSUER_URL is intentionally NOT written here; it is set directly on the
	@# compose service environment block so the in-container URL is always correct.
	@# STATIC_ADMINS=admin@cnak.us:admin matches the Dex staticPasswords entry in
	@# config/dex.dev.yaml — the gateway then issues role=admin (not viewer) when
	@# this email signs in via Dex's password DB.
	@for kv in "OIDC_DEFAULT_PROVIDER=dex" "DEX_CLIENT_ID=artifact-gateway" "DEX_CLIENT_SECRET=dev-dex-client-secret" "STATIC_ADMINS=admin@cnak.us:admin"; do \
		key=$${kv%%=*}; \
		if ! grep -q "^$$key=" "$(ENV_FILE)" 2>/dev/null; then \
			echo "$$kv" >> "$(ENV_FILE)"; \
			echo ">>> wrote $$key to $(ENV_FILE)"; \
		fi; \
	done
	@echo ">>> remember to change ADMIN_BOOTSTRAP_PASSWORD before exposing the gateway"

## dev: bring up postgres + registry + Dex, then run the gateway on the host
dev: dev-init ## start deps (including Dex) and run `go run .`
	@echo ">>> starting postgres + registry + Dex"
	docker compose up -d postgres registry dex
	@echo ">>> waiting for postgres to be healthy"
	@until [ "$$(docker inspect -f '{{.State.Health.Status}}' artifact-gateway-postgres 2>/dev/null)" = "healthy" ]; do \
		sleep 1; \
	done
	@echo ">>> waiting for Dex to respond on http://localhost:5556/.well-known/openid-configuration"
	@until curl -fsS http://localhost:5556/.well-known/openid-configuration >/dev/null 2>&1; do \
		sleep 1; \
	done
	@echo ">>> running artifact-gateway (loading $(ENV_FILE))"
	set -a; . ./$(ENV_FILE); set +a; \
		OIDC_DEFAULT_PROVIDER=dex \
		DEX_ISSUER_URL=http://localhost:5556 \
		DEX_CLIENT_ID=artifact-gateway \
		DEX_CLIENT_SECRET=dev-dex-client-secret \
		go run .

## dev-certs: issue an mkcert-signed cert for local HTTPS dev (idempotent).
##            mkcert's local CA must be trusted by the system once with
##            `mkcert -install` — this target runs it for you the first time.
##            Override the SANs with `make dev-certs DEV_CERT_HOSTS="localhost gw.lvh.me"`.
dev-certs: ## issue ./certs/dev.pem via mkcert
	@if ! command -v mkcert >/dev/null 2>&1; then \
		echo "ERROR: mkcert is not installed."; \
		echo "  macOS:  brew install mkcert nss"; \
		echo "  Linux:  see https://github.com/FiloSottile/mkcert#installation"; \
		exit 1; \
	fi
	@mkdir -p $(CERT_DIR)
	@if [ -s "$(DEV_CERT)" ] && [ -s "$(DEV_KEY)" ]; then \
		echo ">>> $(DEV_CERT) already exists — leaving it alone (delete with \`make dev-certs-clean\`)"; \
	else \
		echo ">>> ensuring mkcert local CA is installed (idempotent)"; \
		mkcert -install; \
		echo ">>> issuing $(DEV_CERT) for: $(DEV_CERT_HOSTS)"; \
		mkcert -cert-file "$(DEV_CERT)" -key-file "$(DEV_KEY)" $(DEV_CERT_HOSTS); \
	fi

## dev-certs-clean: delete ./certs/ (leaves the mkcert root CA installed).
dev-certs-clean: ## remove ./certs/
	rm -rf $(CERT_DIR)

## dev-https: same as `dev`, but serves the public listener over TLS using
##            the mkcert dev cert. Overrides EXTERNAL_HOSTNAME for this run
##            so OIDC callback URLs and OCI bearer JWT `iss` use https://.
##            Production should leave TLS_CERT_FILE/TLS_KEY_FILE unset and
##            terminate TLS at the LB / Cloudflare instead.
dev-https: dev-init dev-certs ## start deps and run `go run .` over HTTPS via mkcert
	@echo ">>> starting postgres + registry"
	docker compose up -d postgres registry
	@echo ">>> waiting for postgres to be healthy"
	@until [ "$$(docker inspect -f '{{.State.Health.Status}}' artifact-gateway-postgres 2>/dev/null)" = "healthy" ]; do \
		sleep 1; \
	done
	@echo ">>> running artifact-gateway over HTTPS (loading $(ENV_FILE))"
	set -a; . ./$(ENV_FILE); set +a; \
		TLS_CERT_FILE="$(abspath $(DEV_CERT))" \
		TLS_KEY_FILE="$(abspath $(DEV_KEY))" \
		EXTERNAL_HOSTNAME="https://localhost:$${PUBLIC_PORT:-8080}" \
		go run .

## dev-dex: DEPRECATED — use `make dev` instead (Dex now starts by default).
##          Kept for back-compat. Equivalent to `make dev`.
dev-dex: dev ## [deprecated] start deps + Dex and run the gateway on HTTP

## dev-stop: stop docker compose services (preserves volumes)
dev-stop: ## stop docker compose services
	docker compose down

## build: compile the Go binary to ./bin/artifact-gateway (requires `make build-ui` first for embed)
build: ## compile Go binary to ./bin/artifact-gateway
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) .

## build-ui: build the React UI into ui/dist (consumed by //go:embed)
build-ui: ## build the React UI
	cd $(UI_SRC) && npm ci --legacy-peer-deps && npm run build

## test: run Go tests
test: ## run go test ./...
	go test ./...

## lint: run go vet (and golangci-lint if installed)
lint: ## run go vet and (optional) golangci-lint
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, skipping"; \
	fi

## image: build the Docker image
image: ## docker build the runtime image
	docker build \
		-f $(DOCKERFILE) \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(IMAGE_TAG) \
		$(DOCKER_CONTEXT)

## compose-up: bring up the full stack (postgres + registry + gateway) in containers
compose-up: ## bring up full stack via docker compose
	docker compose --profile app up -d

## compose-down: tear down compose stack and DELETE volumes
compose-down: ## docker compose down -v (DELETES volumes)
	docker compose down -v

## smoke: end-to-end pull test against the running gateway (see plan §Verification)
smoke: ## run end-to-end docker/helm/oras smoke pulls
	@set -e; \
	: "$${TID:?set TID=<customer token id> for the smoke test}"; \
	: "$${SECRET:?set SECRET=<customer token secret> for the smoke test}"; \
	HOST="$${HOST:-localhost:8080}"; \
	echo ">>> docker login $$HOST"; \
	docker login "$$HOST" -u "$$TID" -p "$$SECRET"; \
	echo ">>> docker pull $$HOST/test/alpine:3.19"; \
	docker pull "$$HOST/test/alpine:3.19"; \
	echo ">>> helm registry login $$HOST"; \
	helm registry login "$$HOST" -u "$$TID" -p "$$SECRET"; \
	echo ">>> helm pull oci://$$HOST/test/mychart --version 0.1.0"; \
	helm pull "oci://$$HOST/test/mychart" --version 0.1.0; \
	echo ">>> oras pull $$HOST/test/binary:v1"; \
	oras pull "$$HOST/test/binary:v1"; \
	echo ">>> crane manifest $$HOST/test/alpine:3.19"; \
	crane manifest "$$HOST/test/alpine:3.19"; \
	echo ">>> smoke OK"

## clean: remove build artifacts (bin/, ui/dist/, certs/)
clean: ## remove bin/, ui/dist/, certs/
	rm -rf $(BIN_DIR) $(UI_DIST) $(CERT_DIR)

## helm-install: helm install the chart into HELM_NAMESPACE (fails if release exists)
helm-install: ## helm install the chart (creates namespace)
	helm install $(HELM_RELEASE) $(HELM_CHART) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		$(HELM_VALUES) $(HELM_ARGS)

## helm-upgrade: helm upgrade --install the chart (idempotent install-or-upgrade)
helm-upgrade: ## helm upgrade --install the chart
	helm upgrade --install $(HELM_RELEASE) $(HELM_CHART) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		$(HELM_VALUES) $(HELM_ARGS)

## helm-uninstall: helm uninstall the release (Postgres data + KEK are NOT removed)
helm-uninstall: ## helm uninstall the release
	helm uninstall $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)
