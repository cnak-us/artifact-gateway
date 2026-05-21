# syntax=docker/dockerfile:1.7
#
# artifact-gateway multi-stage build.
#
# Build from the repo root:
#
#   docker build -t artifact-gateway:dev .
#
# Self-contained: no sibling repositories are required.

ARG VERSION=dev

# -----------------------------------------------------------------------------
# Stage 1: build the embedded React UI
#
# We mirror the host layout (ui/src is the vite project root, dist sits next to it)
# because vite.config.js sets `outDir: '../dist'` relative to itself — meaning the
# build must run from /build/ui/src so the output lands at /build/ui/dist.
#
# Defensive: even if the build context leaks a host node_modules/ (when the
# .dockerignore isn't being honored), we wipe it and reinstall fresh so the
# container has Linux-native binaries.
# -----------------------------------------------------------------------------
FROM node:26-alpine AS ui-builder

WORKDIR /build/ui
COPY ui/src/ ./src/

WORKDIR /build/ui/src
RUN rm -rf node_modules package-lock.json && \
    npm install --legacy-peer-deps --no-audit --no-fund && \
    npm run build
# Output is at /build/ui/dist (because vite outDir is '../dist' from /build/ui/src)

# -----------------------------------------------------------------------------
# Stage 2: build the Go binary
# -----------------------------------------------------------------------------
FROM golang:1.26-alpine AS go-builder

ARG VERSION
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOFIPS140=v1.0.0

RUN apk add --no-cache git ca-certificates

# Bring in module manifests and resolve dependencies (cached layer).
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

# Copy the rest of the Go sources.
COPY . .

# Copy the built UI from the previous stage so `//go:embed all:dist` finds files.
COPY --from=ui-builder /build/ui/dist ./ui/dist

# Build a static, stripped binary.
RUN go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/artifact-gateway \
        .

# -----------------------------------------------------------------------------
# Stage 3: runtime
# -----------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="artifact-gateway" \
      org.opencontainers.image.description="License-gated OCI auth gateway for CNAK artifacts" \
      org.opencontainers.image.source="https://github.com/cnak-us/artifact-gateway" \
      org.opencontainers.image.vendor="cnak-us" \
      org.opencontainers.image.licenses="Apache-2.0"

ENV GODEBUG=fips140=on

COPY --from=go-builder /out/artifact-gateway /usr/local/bin/artifact-gateway

USER nonroot:nonroot

# 8080 = public (OCI, admin/catalog UI, /api/v1)
# 8090 = management (/health/live, /health/ready, /metrics)
EXPOSE 8080 8090

ENTRYPOINT ["/usr/local/bin/artifact-gateway"]
