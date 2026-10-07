# nzr-rules-engine

**Everyone can build an API.** Just point your data source, author flows visually, deploy instantly — no code required.

A **config-driven API orchestration platform** that separates business logic from code. Author flows in a visual CMS, evaluate rules with embedded decision tables, and deploy new APIs without code changes.

```
┌─────────────┐     ┌──────────────────────┐     ┌─────────────────┐
│   Strapi    │────▶│   Config Store (PG)  │◀────│   Go Engine     │
│  CMS + UI   │     │   + Valkey Cache     │     │   (Stateless)   │
└─────────────┘     └──────────────────────┘     └─────────────────┘
   Authors              Publishes to              Reads & Executes
```

## Overview

| Component | Technology | Purpose |
|-----------|------------|---------|
| **Engine** | Go 1.26+, stdlib `net/http` | Stateless flow execution, HTTP routing, connector I/O |
| **Decision Layer** | [GoRules ZEN](https://gorules.io/) (embedded) | Business rule evaluation via decision tables |
| **CMS** | Strapi 5 + custom plugin | Visual flow editor, JDM editor, draft/publish |
| **Connectors** | pgx, valkey-go, http.Client | Resilient data source integrations |

### The Problem It Solves

Instead of writing thousands of lines of nested if/else logic to orchestrate APIs and data sources, this platform shifts business logic to **visual configurations**:

- **Add a new API** → Author a flow in the CMS, no code deploy
- **Change business rules** → Edit decision tables in the visual editor
- **Update routing logic** → Modify the flow tree, publish immediately

### How It Compares

| Tool | Type | State | Rules | Best For |
|------|------|-------|-------|----------|
| **This engine** | Flow orchestrator | Stateless | ZEN (embedded) | Real-time API orchestration |
| Camunda | BPM | Persisted | DMN | Long-running business processes |
| Temporal | Workflow engine | Persisted | None | Durable, fault-tolerant workflows |
| AWS Step Functions | State machine | Persisted | None | Serverless orchestration |

**The gap it fills:** Combines stateless execution + embedded decision tables + resilient connectors in a single binary — otherwise you'd stitch together 3–4 systems.

## Architecture

### Two-Plane Design

```
CONTROL PLANE (Strapi)              DATA PLANE (Go Engine)
─────────────────────────           ─────────────────────────
• Visual flow editor                • Stateless request handler
• JDM decision table editor         • Tree-walking interpreter
• Draft/publish workflow            • Embedded ZEN evaluator
• Multi-environment promotion       • Connection registry + pools
        │                                     ▲
        │ PUBLISH                             │ READ
        ▼                                     │
    ┌───────────────────────────────────────────┐
    │         CONFIG STORE (per environment)    │
    │  PostgreSQL (flows, JDMs, connections)    │
    │  Valkey (hot cache)                       │
    └───────────────────────────────────────────┘
```

- **Control plane** writes validated config on publish
- **Data plane** reads config (cached in Valkey) and executes flows
- **No runtime dependency** — if CMS is down, engine keeps serving

### Flow Model

A flow is a tree of typed nodes executed per request:

```
Trigger (GET /orders/:id)
└─ Action: Postgres → fetch order (ctx.order)
   └─ Condition [ZEN: amount_check]
      ├─ TRUE:  Action: REST → fraud-check API
      │         └─ Response (200, ctx.order + ctx.fraud)
      └─ FALSE: Set → ctx.flags.standard = true
                └─ Response (200, ctx.order)
```

### Node Types (15)

| Category | Nodes | Purpose |
|----------|-------|---------|
| **Entry** | `trigger` | Bind HTTP request to flow context |
| **Control** | `condition`, `switch`, `sequence`, `parallel`, `forEach` | Branching and iteration |
| **Data** | `set`, `decision`, `filter`, `find` | Transform and enrich context |
| **Integration** | `action` | Call external connectors (Postgres, Valkey, REST) |
| **Observability** | `logger` | Structured logging with sampling |
| **Exit** | `response` | Compose HTTP response |

### Decision Integration

Two ways to use ZEN decision tables:

- **`condition` node** — Evaluate rules to pick a branch (true/false)
- **`decision` node** — Evaluate rules to compute values into context

Decision tables are JSON documents (JDMs) authored in the visual editor — business users can edit rules without code deploys.

## Features

### Resilient Connectors

Built-in drivers with shared resilience envelope:

| Type | Operations | Features |
|------|------------|----------|
| `postgres` | query, exec | Connection pooling (pgxpool) |
| `valkey` | get, set, del | Cluster support |
| `rest` / `http` | All HTTP methods | Per-host configuration |

Each connection carries:
- **Timeout** (per-connection, overridable per-node)
- **Retry with backoff** (idempotent operations only)
- **Circuit breaker** (independent per connection)

### Webhooks

Ingest external events and trigger flows:

```
POST /webhooks/{webhook_id}
```

| Provider | Signature Verification | Event Filtering |
|----------|------------------------|-----------------|
| Stripe | `Stripe-Signature` (HMAC-SHA256) | `$.type` filtering |
| GitHub | `X-Hub-Signature-256` | `X-GitHub-Event` header |
| Generic | Configurable header + algorithm | JSONPath-based |

### Scheduled Triggers

Cron-based flow execution:

```json
{
  "scheduleId": "daily-report",
  "schedule": "0 9 * * *",
  "timezone": "Asia/Jakarta",
  "flowId": "generate-report",
  "enabled": true
}
```

Supports standard 5-field cron, `@hourly`/`@daily` aliases, and `@every 5m` syntax.

### Multi-Environment

Isolated config stores per environment (dev/staging/prod):

- Same connection key (`main_db`) resolves to different hosts per env
- **Promotion** = publish flow to next environment's config store
- Engine binary is environment-agnostic (same image, env-specific config)

## CMS Features

The Strapi plugin provides:

### Visual Flow Editor
React Flow-based canvas for authoring flow trees with drag-and-drop nodes.

### JDM Decision Editor
[@gorules/jdm-editor](https://github.com/gorules/jdm-editor) integration for visual decision table authoring.

### Version History
Track changes, compare versions, and rollback flows.

### Validation Panel
Real-time flow validation before publish — invalid flows cannot be activated.

### Environment Selector
Switch between environments, filter views, promote configurations.

## Quick Start

### Prerequisites

- Go 1.22+ with CGO enabled (C toolchain required for ZEN)
- Node.js 22+
- Docker (for Postgres + Valkey)
- PostgreSQL 16
- Valkey 8

### Run with Docker Compose

```bash
# Copy environment template
cp .env.docker.example .env.docker

# Start all services
docker-compose --env-file .env.docker up -d

# Services:
#   - engine:        http://localhost:8080
#   - cms:           http://localhost:1337/admin
#   - postgres-engine, postgres-cms, valkey
```

> **Note:** Engine and CMS use Google Distroless images for minimal attack surface. Healthchecks run via sidecar containers (`engine-healthcheck`, `cms-healthcheck`) since distroless has no shell. For Kubernetes, use native `httpGet` liveness/readiness probes instead.

### Local Development

**Engine (Go):**
```bash
cd engine
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go run ./cmd/engine -addr :8080
```

**CMS (Strapi):**
```bash
cd cms
npm install
npm run dev     # http://localhost:1337/admin
```

### Run Tests

```bash
# Engine unit tests
cd engine && CGO_ENABLED=1 go test ./...

# Engine integration tests (requires Docker)
cd engine && CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi

# CMS tests
cd cms && npm test
```

## Project Structure

```
├── engine/                    Go rules engine (data plane)
│   ├── cmd/engine/            Main entry point
│   ├── internal/
│   │   ├── flow/              Interpreter, context, node handlers
│   │   ├── connect/           Connection registry + resilience
│   │   │   └── drivers/       postgres, valkey, rest connectors
│   │   ├── decision/          ZEN engine (CGO) + stub
│   │   ├── config/            Config store (Postgres + Valkey cache)
│   │   ├── webhook/           Signature verification, filtering
│   │   ├── auth/              JWT + operator token auth
│   │   └── httpapi/           HTTP handlers, admin API
│   └── migrations/            SQL migrations (embedded)
│
├── cms/                       Strapi 5 CMS (control plane)
│   ├── config/                Strapi configuration
│   ├── src/
│   │   ├── api/               Content types (flow, jdm, connection, etc.)
│   │   └── plugins/
│   │       └── rule-engine/   Custom plugin
│   │           ├── admin/     React components (FlowCanvas, JdmEditor, etc.)
│   │           └── server/    Backend services (sync, publish, validate)
│   └── types/                 TypeScript definitions
│
├── docs/                      Architecture documentation
│   ├── hld.md                 High-level design
│   ├── lld.md                 Low-level design
│   ├── lld-contracts.md       Node specs and contracts
│   ├── DEPLOYMENT.md          Deployment guide
│   ├── MONITORING.md          Observability setup
│   └── SECRETS.md             Secret management
│
├── k8s/                       Kubernetes manifests
├── monitoring/                Grafana dashboards, Prometheus config
└── docker-compose.yml         Local development stack
```

## Examples

### Example 1: Order Processing with Business Rules

A flow that fetches an order, evaluates business rules via a ZEN decision table, and routes to different shipping APIs based on the result.

**Flow Tree:**
```
GET /orders/{id}
└─ Action: Postgres query → fetch order
   └─ Condition [ZEN: order-routing]
      ├─ expedited: Action: REST POST /expedite → Set shipping → Response 200
      └─ standard:  Action: REST POST /standard → Set shipping → Response 200
```

**Flow JSON:**
```json
{
  "flowId": "orders-expedite",
  "method": "GET",
  "path": "/orders/{id}",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": {
      "method": "GET",
      "path": "/orders/{id}",
      "input": { "params": ["id"] }
    },
    "children": [{
      "id": "read-order",
      "type": "action",
      "spec": {
        "connection": "orders-pg",
        "operation": {
          "Kind": "query",
          "Payload": {
            "sql": "SELECT amount, status FROM orders WHERE id=$1",
            "params": [{ "value": "{{input.id}}", "as": "int" }]
          },
          "Required": true
        },
        "saveAs": "order"
      },
      "children": [{
        "id": "classify",
        "type": "condition",
        "spec": {
          "jdmId": "order-routing",
          "input": ["order.amount", "order.status"],
          "trueKey": "expedited",
          "falseKey": "standard"
        },
        "children": [
          {
            "id": "expedited",
            "type": "action",
            "spec": {
              "connection": "ship-rest",
              "operation": {
                "Kind": "http",
                "Payload": { "method": "POST", "path": "/expedite", "body": { "order": "{{order}}" } },
                "Required": true
              },
              "saveAs": "shipResult"
            },
            "children": [{
              "id": "respond-expedited",
              "type": "response",
              "spec": { "status": 200, "bodyFrom": "shipResult" }
            }]
          },
          {
            "id": "standard",
            "type": "action",
            "spec": {
              "connection": "ship-rest",
              "operation": {
                "Kind": "http",
                "Payload": { "method": "POST", "path": "/standard", "body": { "order": "{{order}}" } },
                "Required": true
              },
              "saveAs": "shipResult"
            },
            "children": [{
              "id": "respond-standard",
              "type": "response",
              "spec": { "status": 200, "bodyFrom": "shipResult" }
            }]
          }
        ]
      }]
    }]
  }
}
```

**Decision Table (order-routing):**
| amount | status | Result |
|--------|--------|--------|
| > 1000 | paid | expedited |
| * | * | standard |

**Request/Response:**
```bash
curl localhost:8080/orders/123

# Response (expedited: amount > 1000, status = paid)
{
  "tracking_id": "EXP-789",
  "carrier": "express",
  "estimated_days": 1
}
```

---

### Example 2: Payment Status with Dynamic Field Computation

A flow that reads order data and uses a ZEN decision to compute an `action` field based on payment status.

**Flow JSON:**
```json
{
  "flowId": "order-status",
  "method": "GET",
  "path": "/order/{order_id}",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": {
      "method": "GET",
      "path": "/order/{order_id}",
      "input": { "params": ["order_id"] }
    },
    "children": [
      {
        "id": "fetch",
        "type": "action",
        "spec": {
          "connection": "orders-pg",
          "operation": {
            "Kind": "query",
            "Payload": {
              "sql": "SELECT order_id, msisdn, payment_status, payment_amount FROM orders WHERE order_id = $1",
              "params": ["{{input.order_id}}"]
            },
            "Required": true
          },
          "saveAs": "row"
        }
      },
      {
        "id": "decide-action",
        "type": "decision",
        "spec": {
          "jdmId": "payment-action",
          "input": ["row.payment_status"],
          "saveAs": "dec"
        }
      },
      {
        "id": "build-response",
        "type": "set",
        "spec": {
          "targetPath": "",
          "value": {
            "order_id": "{{row.order_id}}",
            "msisdn": "{{row.msisdn}}",
            "payment_status": "{{row.payment_status}}",
            "payment_amount": "{{row.payment_amount}}",
            "action": "{{dec.action}}"
          }
        }
      },
      {
        "id": "respond",
        "type": "response",
        "spec": { "status": 200, "bodyFrom": "" }
      }
    ]
  }
}
```

**Decision Table (payment-action):**
| payment_status | action |
|----------------|--------|
| PAID | proceed_fulfillment |
| * | await_payment |

**Response:**
```json
{
  "order_id": "ORD-123",
  "msisdn": "628111015450",
  "payment_status": "PAID",
  "payment_amount": 199.90,
  "action": "proceed_fulfillment"
}
```

---

### Example 3: Webhook Handler (Stripe)

A webhook configuration that receives Stripe events, verifies the signature, filters for specific event types, and triggers a flow.

**Webhook Configuration:**
```json
{
  "webhookId": "stripe-payments",
  "name": "Stripe Payment Events",
  "provider": "stripe",
  "secretRef": "STRIPE_WEBHOOK_SECRET",
  "flowId": "process-payment",
  "mapping": [
    { "sourceJsonPath": "$.data.object.id", "targetContextPath": "payment_id" },
    { "sourceJsonPath": "$.data.object.amount", "targetContextPath": "amount" },
    { "sourceJsonPath": "$.data.object.currency", "targetContextPath": "currency" }
  ],
  "filter": [
    { "jsonPath": "$.type", "allowedValues": ["payment_intent.succeeded", "payment_intent.failed"] }
  ]
}
```

**Usage:**
```bash
# Stripe sends to:
POST /webhooks/stripe-payments
Headers:
  Stripe-Signature: t=1234567890,v1=abc123...
Body:
  { "type": "payment_intent.succeeded", "data": { "object": { "id": "pi_123", "amount": 5000 } } }

# Engine verifies signature, filters event type, maps fields, triggers flow
```

---

### Example 4: CRUD REST API (Generated from Template)

The engine supports flow templates for common patterns. Here's a generated CRUD API:

**Template Variables:**
```json
{
  "ENTITY": "product",
  "TABLE": "products",
  "CONNECTION": "pg-main",
  "ID_FIELD": "id"
}
```

**Generated Endpoints:**
| Method | Path | Flow ID | Description |
|--------|------|---------|-------------|
| GET | /products | product-list | List all products |
| GET | /products/{id} | product-get | Get product by ID |
| POST | /products | product-create | Create new product |
| PUT | /products/{id} | product-update | Update product |
| DELETE | /products/{id} | product-delete | Delete product |

**Example: List Flow:**
```json
{
  "flowId": "product-list",
  "method": "GET",
  "path": "/products",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": { "method": "GET", "path": "/products" },
    "children": [
      {
        "id": "list-action",
        "type": "action",
        "spec": {
          "connection": "pg-main",
          "operation": {
            "Kind": "query",
            "Payload": { "sql": "SELECT * FROM products" },
            "Required": true
          },
          "saveAs": "rows"
        }
      },
      { "id": "set-items", "type": "set", "spec": { "targetPath": "items", "from": "rows" } },
      { "id": "respond", "type": "response", "spec": { "status": 200 } }
    ]
  }
}
```

---

### Example 5: Scheduled Job (Daily Report)

A cron-triggered flow that runs daily and generates a report.

**Schedule Configuration:**
```json
{
  "scheduleId": "daily-sales-report",
  "name": "Daily Sales Report",
  "schedule": "0 9 * * *",
  "timezone": "Asia/Jakarta",
  "flowId": "generate-sales-report",
  "input": {
    "report_type": "daily",
    "email_to": "reports@example.com"
  },
  "enabled": true
}
```

**Flow (generate-sales-report):**
```json
{
  "flowId": "generate-sales-report",
  "method": "POST",
  "path": "/_internal/reports/sales",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": { "method": "POST", "path": "/_internal/reports/sales" },
    "children": [
      {
        "id": "fetch-data",
        "type": "action",
        "spec": {
          "connection": "analytics-pg",
          "operation": {
            "Kind": "query",
            "Payload": {
              "sql": "SELECT date, total_sales, order_count FROM daily_sales WHERE date = CURRENT_DATE - 1"
            }
          },
          "saveAs": "sales"
        }
      },
      {
        "id": "send-email",
        "type": "action",
        "spec": {
          "connection": "email-api",
          "operation": {
            "Kind": "http",
            "Payload": {
              "method": "POST",
              "path": "/send",
              "body": {
                "to": "{{input.email_to}}",
                "subject": "Daily Sales Report",
                "data": "{{sales}}"
              }
            }
          }
        }
      },
      { "id": "respond", "type": "response", "spec": { "status": 200 } }
    ]
  }
}
```

---

### Example 6: Connection Configuration

Connections define reusable data sources with built-in resilience:

**Postgres Connection:**
```json
{
  "connectionId": "orders-pg",
  "name": "Orders Database",
  "type": "postgres",
  "settings": {
    "host": "db.example.com",
    "port": 5432,
    "database": "orders",
    "user": "app_user",
    "secretRef": "ORDERS_DB_PASSWORD",
    "pool": { "maxConns": 20, "minConns": 5 },
    "timeout": "5s"
  },
  "resilience": {
    "retry": { "maxAttempts": 3, "backoff": "exponential", "initialDelay": "100ms" },
    "circuitBreaker": { "threshold": 5, "timeout": "30s" }
  }
}
```

**REST Connection:**
```json
{
  "connectionId": "ship-rest",
  "name": "Shipping API",
  "type": "rest",
  "settings": {
    "baseUrl": "https://shipping.example.com/api/v1",
    "headers": {
      "Authorization": "Bearer {{env.SHIPPING_API_KEY}}"
    },
    "timeout": "10s"
  },
  "resilience": {
    "retry": { "maxAttempts": 2, "retryableStatuses": [502, 503, 504] },
    "circuitBreaker": { "threshold": 10, "timeout": "60s" }
  }
}
```

---

### Running the Examples

```bash
# Start the stack
docker-compose up -d

# Seed example data (optional)
psql $CONFIG_DSN < engine/internal/config/testdata/seed.sql

# Test the order endpoint
curl localhost:8080/orders/1

# Check health
curl localhost:8080/readyz
```

## API Reference

### Data Plane (Engine)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/{flow-path}` | * | Execute flow matching path + method |
| `/webhooks/{id}` | POST | Webhook ingestion |
| `/livez` | GET | Liveness probe |
| `/readyz` | GET | Readiness probe (checks config store + cache) |
| `/metrics` | GET | Prometheus metrics |

### Admin API (Engine)

Operator-token authenticated endpoints for CMS → Engine communication:

| Endpoint | Description |
|----------|-------------|
| `POST /admin/flows` | Create/update flow |
| `POST /admin/flows/{id}/validate` | Validate flow |
| `POST /admin/flows/{id}/publish` | Publish flow |
| `GET /admin/flows/{id}/versions` | List versions |
| `POST /admin/jdms` | Create/update JDM |
| `POST /admin/connections` | Create/update connection |
| `POST /admin/webhooks` | Create/update webhook |
| `POST /admin/schedules` | Create/update schedule |

## Configuration

### Engine Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `ENGINE_ADDR` | HTTP listen address | `:8080` |
| `CONFIG_DSN` | Postgres connection string | required |
| `VALKEY_ADDR` | Valkey address | required |
| `ADMIN_ENABLED` | Enable admin API | `true` |
| `ADMIN_TOKENS` | Comma-separated operator tokens | required |

### CMS Environment Variables

| Variable | Description |
|----------|-------------|
| `ADMIN_API_BASE_URL` | Engine admin API URL |
| `ADMIN_API_OPERATOR_TOKEN` | Token for engine admin API |
| `CMS_DB_*` | Strapi database configuration |

See `docs/DEPLOYMENT.md` for complete configuration reference.

## Observability

- **Metrics**: Prometheus (RED metrics, connection pool stats, circuit breaker state)
- **Logging**: Structured JSON (`log/slog`)
- **Tracing**: OpenTelemetry (spans for flow walk, node execution, connector calls)

See `docs/MONITORING.md` for Grafana dashboard setup.

## Build Notes

### CGO Requirement

The ZEN decision engine requires CGO for the native library:

```bash
CGO_ENABLED=1 go build ./...
```

Cross-compilation requires a C cross-compiler. The container image must use glibc (Debian/Ubuntu), not musl (Alpine).

### Container Images

Both Engine and CMS use multi-stage builds with **Google Distroless** runtime images for minimal attack surface:

| Component | Base Image | Size | Features |
|-----------|------------|------|----------|
| **Engine** | `gcr.io/distroless/base-debian12:nonroot` | ~82MB | glibc, CA certs, non-root (uid 65532) |
| **CMS** | `gcr.io/distroless/nodejs22-debian12:nonroot` | ~400MB | Node.js 22, non-root |

**Distroless benefits:**
- No shell, no package manager → minimal attack surface
- Non-root by default (`nonroot` tag)
- Smaller images (no OS utilities)
- CVE-free base layer (fewer packages to patch)

**Trade-offs:**
- Cannot exec into container for debugging (use `:debug` tag temporarily)
- No curl/wget for healthchecks (use sidecar or Kubernetes probes)

```dockerfile
# Engine Dockerfile (simplified)
FROM golang:1-bookworm AS builder
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /engine ./cmd/engine

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=builder /engine /engine
ENTRYPOINT ["/engine"]
```

```dockerfile
# CMS Dockerfile (simplified)
FROM node:22-bookworm-slim AS builder
RUN npm ci && npm run build

FROM gcr.io/distroless/nodejs22-debian12:nonroot
COPY --from=builder /app/dist ./dist
COPY --from=builder /app/node_modules ./node_modules
CMD ["dist/server.js"]
```

**Debugging distroless containers:**
```bash
# Use debug variant (includes busybox shell)
docker run -it --entrypoint=sh gcr.io/distroless/base-debian12:debug

# Or inspect from outside
docker logs <container>
docker cp <container>:/path/to/file ./local
```

## Contributing

1. Read the architecture docs in `docs/`
2. Follow Go conventions per `go.dev/doc/effective_go`
3. Run `make vet` and `make test` before submitting
4. Add tests for new node types or connectors

## License

MIT
