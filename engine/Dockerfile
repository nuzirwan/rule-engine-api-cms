# =============================================================================
# Stage 1: Builder — compile Go binary with CGO enabled (ZEN requires it)
# =============================================================================
# Use golang:1-bookworm (not a fixed minor) per project go.mod requiring go 1.26+.
# GOTOOLCHAIN=auto (the default) allows the toolchain to upgrade as needed.
FROM golang:1-bookworm AS builder

# CGO deps: gcc + libc-dev are pre-installed on golang:bookworm, but explicit is
# better for reproducibility per [[ci-cd-and-delivery]].
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Copy dependency manifests first for layer caching (less-frequently-changing
# layers first per [[ci-cd-and-delivery]]).
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build with CGO (ADR-003: ZEN requires CGO).
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /engine ./cmd/engine

# =============================================================================
# Stage 2: Runtime — Google Distroless (minimal attack surface)
# =============================================================================
# distroless/base-debian12 includes glibc, CA certificates, and tzdata.
# No shell, no package manager — minimal attack surface per [[security-and-authz]].
FROM gcr.io/distroless/base-debian12:nonroot

WORKDIR /app

# Copy only the binary from builder (multi-stage minimizes image per
# [[ci-cd-and-delivery]]).
COPY --from=builder /engine /engine

# Default config per [[config-and-secrets]] — all config via env at runtime.
# These are documented defaults; actual values injected at deployment.
ENV ENGINE_ADDR=":8080"
# CONFIG_DSN, CONFIG_SCHEMA, VALKEY_ADDR intentionally unset — optional config.

EXPOSE 8080

# Note: HEALTHCHECK not supported in distroless (no curl/shell).
# Use Kubernetes liveness/readiness probes or Docker Compose healthcheck
# with a sidecar/external checker against /readyz endpoint.

ENTRYPOINT ["/engine"]
