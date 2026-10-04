# =============================================================================
# Stage 1: Builder — compile Go binary with CGO enabled (ZEN requires it)
# =============================================================================
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
# Stage 2: Runtime — minimal glibc-based image (ADR-003/R9: NOT alpine/musl)
# =============================================================================
FROM debian:bookworm-slim

# Install CA certificates for TLS connections (data sources, JWKS, etc.) and curl
# for health checks per [[security-and-authz]]. No other runtime deps needed — the
# binary dynamically links glibc which bookworm-slim provides.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Non-root user per [[security-and-authz]] (least privilege).
RUN groupadd -r -g 1000 engine && useradd -r -u 1000 -g engine engine

WORKDIR /app

# Copy only the binary from builder (multi-stage minimizes image per
# [[ci-cd-and-delivery]]).
COPY --from=builder /engine /engine

# Runtime ownership — the binary is read-only; the user needs no write access.
RUN chown engine:engine /engine

USER engine

# Default config per [[config-and-secrets]] — all config via env at runtime.
# These are documented defaults; actual values injected at deployment.
ENV ENGINE_ADDR=":8080"
# CONFIG_DSN, CONFIG_SCHEMA, VALKEY_ADDR intentionally unset — optional config.

EXPOSE 8080

# Health check against /readyz (readiness probe, 2s timeout per ops.go).
# Interval 30s, timeout 5s, start period 5s, retries 3.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD curl -sf http://localhost:8080/readyz || exit 1

ENTRYPOINT ["/engine"]
