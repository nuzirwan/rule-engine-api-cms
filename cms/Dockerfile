# Production Dockerfile for Strapi 5 CMS
# Multi-stage build with Google Distroless runtime (minimal attack surface)

# =============================================================================
# Stage 1: Build
# Installs all dependencies and builds the TypeScript/admin panel
# =============================================================================
FROM node:22-bookworm-slim AS builder

WORKDIR /app

# Install build dependencies for native modules (Sharp/libvips)
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    libvips-dev \
    python3 \
    && rm -rf /var/lib/apt/lists/*

# Copy package files first (layer caching)
COPY package.json package-lock.json ./

# Install ALL dependencies (including devDependencies for build)
# NOTE: Do NOT set NODE_ENV=production before npm ci — it skips devDeps
RUN npm ci

# Copy source files
COPY tsconfig.json tsconfig.server.json ./
COPY config/ ./config/
COPY src/ ./src/
COPY public/ ./public/
COPY database/ ./database/
COPY types/ ./types/
COPY favicon.png ./

# Build the application (compiles TypeScript + admin panel)
ENV NODE_ENV=production
RUN npm run build

# =============================================================================
# Stage 2: Production Dependencies
# Install only production dependencies (no devDeps) for smaller image
# Sharp uses prebuilt binaries — no native compilation needed at runtime.
# =============================================================================
FROM node:22-bookworm-slim AS deps

WORKDIR /app

# Install build dependencies for native modules (Sharp/libvips)
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    libvips-dev \
    python3 \
    && rm -rf /var/lib/apt/lists/*

# Copy package files
COPY package.json package-lock.json ./

# Install production dependencies only (no devDependencies)
# Sharp bundles its own libvips binary for distroless compatibility.
ENV NODE_ENV=production
RUN npm ci --omit=dev

# =============================================================================
# Stage 3: Runtime — Google Distroless Node.js (minimal attack surface)
# =============================================================================
# distroless/nodejs22-debian12 includes Node.js 22 + minimal glibc.
# No shell, no package manager — minimal attack surface per [[security-and-authz]].
# Sharp works because it bundles prebuilt libvips binaries since v0.32.0.
FROM gcr.io/distroless/nodejs22-debian12:nonroot

WORKDIR /app

# Copy package.json (needed by Node.js for module resolution)
COPY --chown=nonroot:nonroot package.json ./

# Copy production node_modules from deps stage
COPY --chown=nonroot:nonroot --from=deps /app/node_modules/ ./node_modules/

# Copy built artifacts from builder
COPY --chown=nonroot:nonroot --from=builder /app/dist/ ./dist/
COPY --chown=nonroot:nonroot --from=builder /app/config/ ./config/
COPY --chown=nonroot:nonroot --from=builder /app/public/ ./public/
COPY --chown=nonroot:nonroot --from=builder /app/database/ ./database/
COPY --chown=nonroot:nonroot --from=builder /app/favicon.png ./

# Strapi needs src/ at runtime for plugin registration and admin customization
COPY --chown=nonroot:nonroot --from=builder /app/src/ ./src/

# Copy tsconfig files (may be needed by runtime)
COPY --chown=nonroot:nonroot --from=builder /app/tsconfig.json ./
COPY --chown=nonroot:nonroot --from=builder /app/tsconfig.server.json ./

# Copy types (Strapi may need these at runtime)
COPY --chown=nonroot:nonroot --from=builder /app/types/ ./types/

# Set environment
ENV NODE_ENV=production

# Expose Strapi default port
EXPOSE 1337

# Note: HEALTHCHECK not supported in distroless (no curl/shell).
# Use Kubernetes liveness/readiness probes or Docker Compose healthcheck
# with a sidecar/external checker against /_health endpoint.

# Start Strapi — distroless nodejs image uses node as entrypoint
# We run the compiled dist/server.js directly (Strapi 5 output)
CMD ["dist/server.js"]
