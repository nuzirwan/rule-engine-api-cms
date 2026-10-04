# Docker Compose Setup

Multi-environment Docker Compose configuration for the nzr-rules-engine stack.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        nzr-internal network                      │
│                                                                  │
│  ┌──────────┐    ┌──────────┐    ┌──────────────────────────┐   │
│  │  engine  │◄───│   cms    │    │ (external data sources)  │   │
│  │  :8080   │    │  :1337   │    │  ORDERS_PG, SHIP_REST    │   │
│  └────┬─────┘    └────┬─────┘    └──────────────────────────┘   │
│       │               │                                          │
│       │               │                                          │
│  ┌────▼─────┐    ┌────▼─────┐                                   │
│  │postgres- │    │postgres- │                                   │
│  │ engine   │    │   cms    │                                   │
│  │ :5432    │    │  :5432   │                                   │
│  └──────────┘    └──────────┘                                   │
│       │                                                          │
│  ┌────▼─────┐                                                   │
│  │  valkey  │                                                   │
│  │  :6379   │                                                   │
│  └──────────┘                                                   │
└─────────────────────────────────────────────────────────────────┘
```

## Services

| Service | Description | Internal Port | External Port |
|---------|-------------|---------------|---------------|
| engine | Go rules engine | 8080 | 8080 |
| cms | Strapi 5 CMS | 1337 | 1337 |
| postgres-engine | Engine config store | 5432 | 5433 (dev only) |
| postgres-cms | CMS data store | 5432 | 5434 (dev only) |
| valkey | Engine config cache | 6379 | 6379 (dev only) |

## Quick Start

### 1. Setup Environment

```bash
cp .env.docker.example .env.docker
# Edit .env.docker — fill in your secrets
```

### 2. Generate Secrets

```bash
# Generate Strapi keys
openssl rand -base64 32  # for APP_KEYS (need 2)
openssl rand -hex 32     # for salts and secrets

# Generate admin token and its hash
TOKEN=$(openssl rand -hex 32)
echo "Token: $TOKEN"
echo -n "$TOKEN" | sha256sum | cut -d' ' -f1  # for ADMIN_TOKENS
```

### 3. Run the Stack

Development (with exposed DB ports):
```bash
docker-compose --env-file .env.docker up -d
```

Production (isolated DB ports):
```bash
docker-compose -f docker-compose.yml -f docker-compose.prod.yml --env-file .env.docker up -d
```

### 4. Verify

```bash
# Check service health
docker-compose ps

# View logs
docker-compose logs -f engine
docker-compose logs -f cms

# Test engine health
curl http://localhost:8080/healthz

# Test CMS health
curl http://localhost:1337/_health
```

## Environment Variables

### Engine

| Variable | Description | Default |
|----------|-------------|---------|
| `ENGINE_DB_NAME` | Postgres database name | `rule_engine` |
| `ENGINE_DB_USER` | Postgres username | `rule_engine` |
| `ENGINE_DB_PASSWORD` | Postgres password | **REQUIRED** |
| `ENGINE_CONFIG_SCHEMA` | Postgres schema | `rule_engine` |
| `ADMIN_ENABLED` | Enable admin API | `true` |
| `ADMIN_TOKENS` | Operator token allow-list | **REQUIRED for admin** |
| `ORDERS_PG_DSN` | Flow data source DSN | (optional) |
| `SHIP_REST_BASE_URL` | Flow REST endpoint | (optional) |

### CMS

| Variable | Description | Default |
|----------|-------------|---------|
| `CMS_DB_NAME` | Postgres database name | `strapi_cms` |
| `CMS_DB_USER` | Postgres username | `strapi_cms` |
| `CMS_DB_PASSWORD` | Postgres password | **REQUIRED** |
| `CMS_DB_SCHEMA` | Postgres schema | `strapi_cms` |
| `CMS_APP_KEYS` | Strapi app keys (comma-sep) | **REQUIRED** |
| `CMS_API_TOKEN_SALT` | API token salt | **REQUIRED** |
| `CMS_ADMIN_JWT_SECRET` | Admin JWT secret | **REQUIRED** |
| `CMS_JWT_SECRET` | JWT secret | **REQUIRED** |
| `CMS_TRANSFER_TOKEN_SALT` | Transfer token salt | **REQUIRED** |
| `CMS_ENCRYPTION_KEY` | Encryption key (32 chars) | **REQUIRED** |
| `CMS_ADMIN_API_OPERATOR_TOKEN` | Engine admin API token | (for publish) |

## File Structure

```
docker-compose.yml          # Base configuration
docker-compose.override.yml # Development overrides (auto-loaded)
docker-compose.prod.yml     # Production overrides
.env.docker.example         # Environment template
```

## Data Persistence

Named volumes persist data across container restarts:

| Volume | Service | Path |
|--------|---------|------|
| `postgres-engine-data` | postgres-engine | `/var/lib/postgresql/data` |
| `postgres-cms-data` | postgres-cms | `/var/lib/postgresql/data` |
| `valkey-data` | valkey | `/data` |

To reset data:
```bash
docker-compose down -v  # WARNING: deletes all data
```

## Multi-Environment Support

### Development (default)

`docker-compose.override.yml` is auto-loaded, providing:
- Exposed database ports for local tooling
- Volume mounts for hot-reload
- `NODE_ENV=development` for CMS

### Production

Use explicit override to skip dev settings:
```bash
docker-compose -f docker-compose.yml -f docker-compose.prod.yml --env-file .env.docker up -d
```

Production override provides:
- No exposed database ports
- Resource limits (CPU/memory)
- Strict restart policy (`always`)
- `NODE_ENV=production` for CMS

### Custom Environment

Create your own override file:
```bash
# docker-compose.staging.yml
docker-compose -f docker-compose.yml -f docker-compose.staging.yml --env-file .env.staging up -d
```

## Troubleshooting

### Engine won't start

1. Check postgres-engine is healthy: `docker-compose ps postgres-engine`
2. Check logs: `docker-compose logs engine`
3. Verify CONFIG_DSN resolves internally: connection uses `postgres-engine` hostname

### CMS won't start

1. Check postgres-cms is healthy: `docker-compose ps postgres-cms`
2. Verify all Strapi secrets are set (APP_KEYS, *_SECRET, *_SALT)
3. Check logs: `docker-compose logs cms`

### Database connection refused

Internal services use Docker DNS names (`postgres-engine`, `postgres-cms`).
External connections (dev) use localhost:5433/5434.

### Health check failing

- Engine: `curl http://localhost:8080/healthz` (requires running server)
- CMS: Strapi needs ~60s startup time before `/_health` responds

## Security Notes

Per CI/CD & Delivery standards:
- No secrets baked into images — config via env at runtime
- Non-root users in containers
- Internal network isolates databases from public access
- Production override removes exposed DB ports
