# Production Dockerfile for Strapi 5 CMS
# Multi-stage build: builder + runtime

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
ENV NODE_ENV=production
RUN npm ci --omit=dev

# =============================================================================
# Stage 3: Runtime
# Minimal image with only what's needed to run
# =============================================================================
FROM node:22-bookworm-slim AS runtime

WORKDIR /app

# Install runtime dependencies only (libvips for Sharp)
RUN apt-get update && apt-get install -y --no-install-recommends \
    libvips42 \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Copy package files
COPY package.json package-lock.json ./

# Copy production node_modules from deps stage (no npm ci needed)
COPY --from=deps /app/node_modules/ ./node_modules/

# Copy built artifacts from builder
COPY --from=builder /app/dist/ ./dist/
COPY --from=builder /app/config/ ./config/
COPY --from=builder /app/public/ ./public/
COPY --from=builder /app/database/ ./database/
COPY --from=builder /app/favicon.png ./

# Strapi needs src/ at runtime for plugin registration and admin customization
COPY --from=builder /app/src/ ./src/

# Copy tsconfig files (may be needed by runtime)
COPY --from=builder /app/tsconfig.json ./
COPY --from=builder /app/tsconfig.server.json ./

# Copy types (Strapi may need these at runtime)
COPY --from=builder /app/types/ ./types/

# Set environment
ENV NODE_ENV=production

# Create non-root user
RUN groupadd --gid 1001 strapi \
    && useradd --uid 1001 --gid strapi --shell /bin/bash --create-home strapi \
    && chown -R strapi:strapi /app

# Create uploads directory with correct permissions
RUN mkdir -p /app/public/uploads && chown -R strapi:strapi /app/public/uploads

# Switch to non-root user
USER strapi

# Expose Strapi default port
EXPOSE 1337

# Health check — Strapi 5 has built-in /_health endpoint
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD curl -f http://localhost:1337/_health || exit 1

# Start Strapi
CMD ["npm", "run", "start"]
