# Deployment Guide — nzr-rules-engine Stack

Environment variable reference for the rules engine and CMS. This is the canonical source of
truth; `DOCKER.md` covers container orchestration on top of this.

> **Security rule**: never hard-code secrets in images, code, or version control. Config via env
> at runtime (Twelve-Factor III). Secrets must come from a secret manager in production — see
> `SECRETS.md`.

## Legend

| Symbol | Meaning |
|--------|---------|
| ✅ Required | Must be set; engine/CMS refuses to start or behaves incorrectly without it |
| ⚠️ Conditional | Required only when a feature flag is enabled |
| 🔒 Secret | Contains a credential — store in Vault/SSM, never in a ConfigMap or unencrypted env file |
| ⚙️ Config | Non-sensitive; safe in ConfigMap / plain env |
| (empty default) | Variable is optional; omit to use built-in default |

---

## Engine (`nzr-rules-engine`)

The engine process reads its configuration exclusively from environment variables (and an optional
local `.env` file for development). Process environment always wins over the `.env` file.

### Config Store

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `CONFIG_DSN` | ✅ (prod) | 🔒 | *(empty → in-memory)* | Full Postgres DSN for the engine's own config store. Omit to run in-memory (dev/test only — no DB is touched, seed.json is loaded into a memStore). Example: `postgres://rule_engine:pass@127.0.0.1:5432/rule_engine?sslmode=disable` |
| `CONFIG_SCHEMA` | | ⚙️ | `rule_engine` | Dedicated Postgres schema for config tables. Must be a plain SQL identifier. Config tables are always isolated from `public` and from application schemas. |
| `VALKEY_ADDR` | | ⚙️ | *(empty → cache disabled)* | Address of the Valkey/Redis cache used to front the config store. Omit to run the config store cache-down (every read hits Postgres directly). In-memory mode never uses this. Example: `127.0.0.1:6379` |

### HTTP Server

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `ENGINE_ADDR` | | ⚙️ | `:8080` | TCP listen address for the engine's HTTP server. Overrides the `-addr` command-line flag. Example: `:8080` or `0.0.0.0:9090` |

### Admin Control Plane

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `ADMIN_ENABLED` | | ⚙️ | *(disabled)* | Set to `true` to mount the `/admin` control-plane routes. Any other value (or unset) mounts the admin plane **closed** — every `/admin/*` request returns 503. Setting `true` requires a valid `ADMIN_TOKENS` list or the engine refuses to boot. |
| `ADMIN_TOKENS` | ⚠️ | 🔒 | — | Operator token allow-list. Required when `ADMIN_ENABLED=true`; a missing or malformed list is a fatal boot error. Format: semicolon-separated entries `<sha256hex>:<subject>:<roles>`. Only the SHA-256 hash of each token is stored — the plaintext is never persisted or logged. Roles: `flow.write`, `flow.publish`, `flow.read`. Example entry: `<64-char-hex>:op:strapi:flow.read,flow.write,flow.publish` |

### Data Plane Connections

These override the connection endpoints baked into `seed.json` so flows can reach real data
sources without editing the seed.

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `ORDERS_PG_DSN` | | 🔒 | *(use seed value)* | Postgres DSN for the `orders-pg` connection (the flow reads order rows from here). Example: `postgres://user:pass@127.0.0.1:5432/orders?sslmode=disable` |
| `SHIP_REST_BASE_URL` | | ⚙️ | *(use seed value)* | Base URL for the `ship-rest` connection (flow POSTs `/expedite` or `/standard` here). Example: `http://shipping-service:8899` |

### Development-Only

These variables apply to local development and the test harness; they are ignored by the Docker
Compose stack.

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `RUN_REST_STUB` | | ⚙️ | *(off)* | Set to `1` to start the in-process REST stub that answers `/expedite` and `/standard`. Only meaningful when running the engine binary directly (not in Docker). |
| `REST_STUB_PORT` | | ⚙️ | `8899` | Port the REST stub listens on (must match `SHIP_REST_BASE_URL`'s port). |

### Docker Compose — Engine Host Variables

These are used by `docker-compose.yml` to construct the `CONFIG_DSN` at startup; they are not
read directly by the engine binary.

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `ENGINE_DB_NAME` | | ⚙️ | `rule_engine` | Postgres database name for the engine config store. |
| `ENGINE_DB_USER` | | ⚙️ | `rule_engine` | Postgres username for the engine config store. |
| `ENGINE_DB_PASSWORD` | ✅ | 🔒 | — | Postgres password for the engine config store. |
| `ENGINE_CONFIG_SCHEMA` | | ⚙️ | `rule_engine` | Forwarded as `CONFIG_SCHEMA` inside the container. |
| `ENGINE_TAG` | | ⚙️ | `latest` | Docker image tag for the engine service. |
| `ENGINE_PORT` | | ⚙️ | `8080` | Host port mapped to the engine container's port 8080. |
| `ENGINE_DB_PORT` | | ⚙️ | `5433` | Host port for the engine's Postgres (dev override only; not exposed in prod). |

---

## CMS (`nzr-strapi-cms`)

The CMS is a Strapi 5 application. It reads its configuration from environment variables via the
Strapi `env()` helper in `config/*.ts`. All Strapi secrets must be generated and kept unique per
environment — never reuse example placeholders.

### Server

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `HOST` | | ⚙️ | `0.0.0.0` | CMS HTTP server bind address. |
| `PORT` | | ⚙️ | `1337` | CMS HTTP server port. |
| `NODE_ENV` | | ⚙️ | `development` | Node environment. Set to `production` in production. |

### Strapi Application Secrets

These are required by Strapi for session signing, JWT signing, API token salting, and data
encryption. All must be non-empty in production.

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `APP_KEYS` | ✅ | 🔒 | — | Comma-separated list of at least 2 random keys used for session cookie signing. Generate: `openssl rand -base64 32`. Example (redacted): `<base64-key-1>,<base64-key-2>` |
| `API_TOKEN_SALT` | ✅ | 🔒 | — | Salt used to hash API tokens stored in the database. Generate: `openssl rand -hex 32`. |
| `ADMIN_JWT_SECRET` | ✅ | 🔒 | — | Secret used to sign admin panel JWTs. Generate: `openssl rand -base64 32`. |
| `JWT_SECRET` | ✅ | 🔒 | — | Secret used to sign end-user JWTs (Users & Permissions plugin). Generate: `openssl rand -base64 32`. |
| `TRANSFER_TOKEN_SALT` | ✅ | 🔒 | — | Salt used to hash data transfer tokens. Generate: `openssl rand -hex 32`. |
| `ENCRYPTION_KEY` | ✅ | 🔒 | — | 32-character key used to encrypt sensitive fields in the database. **Critical**: if this changes, encrypted data cannot be recovered. Generate: `openssl rand -hex 16` (32 hex chars). |

### CMS Database

The CMS owns a **separate** Postgres database and schema from the engine. It must never write to
`public` or to the engine's `rule_engine` schema.

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `DATABASE_CLIENT` | | ⚙️ | `postgres` | Database driver. Supported values: `postgres`, `mysql`, `sqlite` (sqlite is local dev only). |
| `CMS_DB_HOST` | | ⚙️ | `localhost` | Database host. In Docker Compose, set to `postgres-cms`. |
| `CMS_DB_PORT` | | ⚙️ | `5432` | Database port. |
| `CMS_DB_NAME` | | ⚙️ | `strapi_cms` | Database name. |
| `CMS_DB_USER` | | ⚙️ | `strapi_cms` | Database username. |
| `CMS_DB_PASSWORD` | ✅ | 🔒 | — | Database password. Required in production. |
| `CMS_DB_SCHEMA` | | ⚙️ | `strapi_cms` | Postgres schema for CMS tables. Never use `public` or `rule_engine`. |
| `CMS_DB_SSL` | | ⚙️ | `false` | Enable TLS for the database connection. Set to `true` in production with a managed database. |
| `CMS_DB_SSL_KEY` | | 🔒 | — | Client TLS private key (PEM). Only used when `CMS_DB_SSL=true`. |
| `CMS_DB_SSL_CERT` | | 🔒 | — | Client TLS certificate (PEM). Only used when `CMS_DB_SSL=true`. |
| `CMS_DB_SSL_CA` | | 🔒 | — | CA certificate (PEM) for server verification. Only used when `CMS_DB_SSL=true`. |
| `CMS_DB_SSL_REJECT_UNAUTHORIZED` | | ⚙️ | `true` | Reject connections with invalid server certificates. **Never set to `false` in production.** |
| `CMS_DB_POOL_MIN` | | ⚙️ | `2` | Minimum database connection pool size. |
| `CMS_DB_POOL_MAX` | | ⚙️ | `10` | Maximum database connection pool size. |
| `DATABASE_CONNECTION_TIMEOUT` | | ⚙️ | `60000` | Connection acquire timeout (ms). |
| `DATABASE_FILENAME` | | ⚙️ | `.tmp/data.db` | SQLite filename. Only used when `DATABASE_CLIENT=sqlite` (dev only). |

### CMS → Engine Connection

The CMS publishes flows to the engine via the admin HTTP API. These variables configure that
connection.

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `ADMIN_API_BASE_URL` | ⚠️ | ⚙️ | — | Base URL of the engine admin API. Required when the CMS needs to publish flows. In Docker Compose, set to `http://engine:8080`. |
| `ADMIN_API_OPERATOR_TOKEN` | ⚠️ | 🔒 | — | Plaintext operator token sent as `Authorization: Bearer <token>` on every admin API call. Its SHA-256 hash must be in the engine's `ADMIN_TOKENS`. Required when CMS publishes to engine. |
| `ADMIN_API_ENV` | | ⚙️ | `""` | Payload `env` field sent in admin API requests. Selects the target rule environment (e.g., `prod`, `staging`). Leave empty for the default environment. |

### CMS Strapi Feature Flags

These control optional Strapi admin UI behaviour.

| Variable | Req | Secret | Default | Description |
|----------|-----|--------|---------|-------------|
| `FLAG_NPS` | | ⚙️ | `true` | Show NPS survey in admin panel. |
| `FLAG_PROMOTE_EE` | | ⚙️ | `true` | Show Strapi Enterprise Edition promotions in admin panel. |
| `FLAG_DOC_LINKS` | | ⚙️ | `true` | Show documentation links in admin panel. |
| `WEBHOOKS_POPULATE_RELATIONS` | | ⚙️ | `false` | Include relation data in Strapi webhook payloads. |

### Docker Compose — CMS Host Variables

These are host-side variables consumed by `docker-compose.yml`; they are forwarded into the CMS
container as the corresponding container-internal variable names.

| Variable | Req | Secret | Container Var | Default | Description |
|----------|-----|--------|---------------|---------|-------------|
| `CMS_TAG` | | ⚙️ | *(image tag)* | `latest` | Docker image tag for the CMS service. |
| `CMS_PORT` | | ⚙️ | *(port map)* | `1337` | Host port mapped to the CMS container's port 1337. |
| `CMS_DB_PORT` | | ⚙️ | *(port map)* | `5434` | Host port for the CMS's Postgres (dev override only; not exposed in prod). |
| `CMS_APP_KEYS` | ✅ | 🔒 | `APP_KEYS` | — | Forwarded as `APP_KEYS` inside the CMS container. |
| `CMS_API_TOKEN_SALT` | ✅ | 🔒 | `API_TOKEN_SALT` | — | Forwarded as `API_TOKEN_SALT` inside the CMS container. |
| `CMS_ADMIN_JWT_SECRET` | ✅ | 🔒 | `ADMIN_JWT_SECRET` | — | Forwarded as `ADMIN_JWT_SECRET` inside the CMS container. |
| `CMS_JWT_SECRET` | ✅ | 🔒 | `JWT_SECRET` | — | Forwarded as `JWT_SECRET` inside the CMS container. |
| `CMS_TRANSFER_TOKEN_SALT` | ✅ | 🔒 | `TRANSFER_TOKEN_SALT` | — | Forwarded as `TRANSFER_TOKEN_SALT` inside the CMS container. |
| `CMS_ENCRYPTION_KEY` | ✅ | 🔒 | `ENCRYPTION_KEY` | — | Forwarded as `ENCRYPTION_KEY` inside the CMS container. |
| `CMS_ADMIN_API_OPERATOR_TOKEN` | ⚠️ | 🔒 | `ADMIN_API_OPERATOR_TOKEN` | — | Forwarded as `ADMIN_API_OPERATOR_TOKEN` inside the CMS container. Required when CMS publishes to engine. |
| `CMS_ADMIN_API_ENV` | | ⚙️ | `ADMIN_API_ENV` | — | Forwarded as `ADMIN_API_ENV` inside the CMS container. |

---

## Infrastructure Variables (Docker Compose internal)

| Variable | Service | Default | Description |
|----------|---------|---------|-------------|
| `VALKEY_PORT` | valkey | `6379` | Host port for Valkey (dev override only). |

---

## Quick Start

### Local Development (in-memory engine, no DB)

```bash
# Engine — minimal in-memory mode
cp .env.example .env
# Leave CONFIG_DSN empty; set ENGINE_ADDR if needed
cd engine && CGO_ENABLED=1 go build -o bin/engine ./cmd/engine && ./bin/engine
```

### Local Development (full stack via Docker Compose)

```bash
cp .env.docker.example .env.docker

# Generate secrets
openssl rand -base64 32  # use twice for APP_KEYS (comma-separate)
openssl rand -base64 32  # ADMIN_JWT_SECRET
openssl rand -base64 32  # JWT_SECRET
openssl rand -hex 32     # API_TOKEN_SALT
openssl rand -hex 32     # TRANSFER_TOKEN_SALT
openssl rand -hex 16     # ENCRYPTION_KEY (32 hex chars)

# Generate engine admin token
TOKEN=$(openssl rand -hex 32)
echo "Token (put in CMS_ADMIN_API_OPERATOR_TOKEN): $TOKEN"
HASH=$(echo -n "$TOKEN" | sha256sum | cut -d' ' -f1)
echo "Hash (put in ADMIN_TOKENS): ${HASH}:op:strapi:flow.read,flow.write,flow.publish"

# Start the stack
docker-compose --env-file .env.docker up -d

# Verify
curl http://localhost:8080/readyz
curl http://localhost:1337/_health
```

### Production

```bash
docker-compose -f docker-compose.yml -f docker-compose.prod.yml \
  --env-file .env.docker up -d
```

---

## Deployment Checklist

### Pre-flight (all environments)

- [ ] All ✅ Required variables are set and non-empty
- [ ] `ENGINE_DB_PASSWORD` is a strong random password (not a placeholder)
- [ ] `CMS_DB_PASSWORD` is a strong random password (not a placeholder)
- [ ] `ADMIN_TOKENS` contains at least one valid entry when `ADMIN_ENABLED=true`
- [ ] All CMS Strapi secrets (`APP_KEYS`, `*_SALT`, `*_SECRET`, `ENCRYPTION_KEY`) are unique to this environment
- [ ] `CMS_ADMIN_API_OPERATOR_TOKEN` plaintext matches a hash entry in `ADMIN_TOKENS`
- [ ] No placeholder values (e.g. `CHANGEME_*`) remain in the environment

### Production-Specific

- [ ] Secrets sourced from Vault or AWS SSM (not plain env files) — see `SECRETS.md`
- [ ] `NODE_ENV=production` for CMS
- [ ] `CMS_DB_SSL=true` and `CMS_DB_SSL_REJECT_UNAUTHORIZED=true` if using a managed database
- [ ] Database ports **not** exposed to the public network (use `docker-compose.prod.yml`)
- [ ] Engine and CMS images pinned to a specific digest or immutable tag (not `latest`)
- [ ] `ENCRYPTION_KEY` backed up securely — losing it makes encrypted CMS data unrecoverable
- [ ] Log aggregation configured — engine emits structured JSON logs to stdout
- [ ] Alerting configured on `/readyz` (engine) and `/_health` (CMS)
- [ ] Resource limits set (see `docker-compose.prod.yml` defaults)

### Post-deploy Smoke Test

```bash
# Engine liveness
curl -f http://<engine-host>:8080/livez

# Engine readiness (gates on config store only, not data sources)
curl -f http://<engine-host>:8080/readyz

# Engine metrics (Prometheus endpoint)
curl -s http://<engine-host>:8080/metrics | grep 'up'

# CMS health
curl -f http://<cms-host>:1337/_health

# Engine admin API (requires a valid operator token)
curl -H "Authorization: Bearer <token>" http://<engine-host>:8080/admin/flows
```

---

## Service Dependency Order

```
postgres-engine ──► engine ──► (serve traffic)
postgres-cms    ──►
                    engine ──► cms ──► (serve UI)
valkey          ──► engine
```

The engine depends on `postgres-engine` (healthy) and `valkey` (healthy). The CMS depends on
`postgres-cms` (healthy) and `engine` (healthy). Startup order is enforced by Docker Compose
`depends_on` conditions. CMS takes ~60 seconds to boot (Strapi build step).

---

## See Also

- `SECRETS.md` — secrets inventory, Vault and SSM integration patterns
- `DOCKER.md` — Docker Compose architecture and troubleshooting
- `docs/hld.md` — high-level design and architectural decisions
