# nzr-rules-engine

A lightweight **stateless flow orchestration engine** with embedded decision tables. It walks a declarative flow tree per HTTP request: read data sources, evaluate business rules via [GoRules ZEN](https://gorules.io/), branch on outcomes, call external services, and compose responses — all in a single request/response cycle with sub-millisecond overhead.

```
HTTP Request → Flow Interpreter → ZEN Decision Tables → Connectors → HTTP Response
```

**Module path:** `nzr-rules-engine` · Go 1.22+ · stdlib `net/http` only (no chi/gin)

## What is this?

This is **not** a BPM engine — it's a purpose-built API orchestration layer that combines:

| Capability | Implementation |
|------------|----------------|
| **Flow orchestration** | Tree-based interpreter with 15 node types |
| **Business rules** | Embedded ZEN decision tables (no external service) |
| **Connector resilience** | Retry, circuit breaker, timeouts per connection |
| **Webhook ingestion** | Stripe/GitHub/generic HMAC with filtering |

### How it compares

| Tool | Type | State | Rules | Best for |
|------|------|-------|-------|----------|
| **This engine** | Flow orchestrator | Stateless | ZEN (embedded) | API orchestration, real-time rules |
| Camunda | BPM | Persisted | DMN | Long-running business processes |
| Temporal | Workflow engine | Persisted | None | Durable, fault-tolerant workflows |
| AWS Step Functions | State machine | Persisted | None | Serverless orchestration |
| Apache Camel | Integration | Optional | External | Enterprise integration patterns |

**The gap it fills:** No existing tool combines stateless execution + embedded decision tables + resilient connectors in a single Go binary. You'd otherwise stitch together 3–4 systems (gateway + rules service + workflow engine + resilience library).

## Flow model

A flow is a tree of typed nodes executed per request:

```
Trigger → Action(postgres) → Condition(ZEN decision)
                                 ├── true:  Action(HTTP POST) → Response
                                 └── false: Set(field) → Response
```

### Node types (15)

| Category | Nodes | Purpose |
|----------|-------|---------|
| **Entry** | `trigger` | Binds request to flow context |
| **Control** | `condition`, `switch`, `sequence`, `parallel`, `forEach` | Branching and iteration |
| **Data** | `set`, `decision`, `filter`, `find`, `map`, `reduce` | Transform and enrich context |
| **Integration** | `action` | Call external connectors |
| **Observability** | `logger` | Structured logging with sampling |
| **Exit** | `response` | Compose HTTP response |

### Decision integration

Two ways to use ZEN decision tables:

- **`condition` node** — Evaluate rules to pick a branch (true/false path)
- **`decision` node** — Evaluate rules to compute values (save to flow context)

Decision tables are JSON documents (JDMs) stored and versioned alongside flows — business users can edit rules without code deploys.

## Connectors

Built-in drivers with shared resilience envelope (timeout → circuit breaker → retry):

| Type | Operations | Pool |
|------|------------|------|
| `postgres` | query, exec, ping | pgxpool |
| `valkey` | get, set, del, ping | valkey-go |
| `rest` / `http` | HTTP methods | http.Client |

Each connection carries its own resilience policy; action nodes can override per-call.

## Webhooks

Ingest external events and trigger flows:

```
POST /webhooks/{webhook_id}
```

| Provider | Signature | Event type |
|----------|-----------|------------|
| Stripe | `Stripe-Signature` (timestamp + HMAC-SHA256) | `$.type` from payload |
| GitHub | `X-Hub-Signature-256` (HMAC-SHA256) | `X-GitHub-Event` header |
| Generic | Configurable header + algorithm | JSONPath extraction |

Webhooks support:
- **Filtering** — JSONPath-based event filtering (skip irrelevant events)
- **Mapping** — Extract payload fields into flow context
- **Logging** — Audit trail with redacted headers and payload hash

## Example flow

```json
{
  "id": "order-flow",
  "tree": {
    "id": "root", "type": "trigger",
    "children": [{
      "id": "fetch", "type": "action",
      "spec": {
        "connection": "orders-db",
        "kind": "query",
        "payload": {"sql": "SELECT amount, status FROM orders WHERE id = $1", "params": ["{{input.orderId}}"]}
      },
      "children": [{
        "id": "route", "type": "condition",
        "spec": {"jdmId": "order-routing", "input": {"amount": "data.fetch[0].amount", "status": "data.fetch[0].status"}},
        "children": [
          {"id": "expedited", "type": "action", "spec": {"connection": "shipping-api", "kind": "http", "payload": {"method": "POST", "path": "/expedite"}}},
          {"id": "standard", "type": "action", "spec": {"connection": "shipping-api", "kind": "http", "payload": {"method": "POST", "path": "/standard"}}}
        ]
      }]
    }]
  }
}
```

The `order-routing` decision table: `amount > 1000 AND status == 'paid' => "expedited"`, else `"standard"`.

## Error classification

Engine errors map to HTTP status at the edge:

| Error class | HTTP status | Meaning |
|-------------|-------------|---------|
| `Validation` | 400 | Bad request / invalid flow |
| `NotFound` | 404 | Missing resource |
| `Timeout` | 504 | Deadline exceeded |
| `Upstream` | 502 | Connector failure |
| `Internal` | 500 | Engine bug |

## Build and run

**CGO is required.** The decision engine links the embedded ZEN native library
(`github.com/gorules/zen-go/v2`), so a C toolchain (`gcc`/`cc`) must be present
and `CGO_ENABLED=1` set. A `CGO_ENABLED=0` build compiles via the `//go:build !cgo`
stub but **refuses ZEN at runtime** with a Validation error.

```sh
# From repo root (Makefile handles cd)
make build      # Build the engine
make test       # Unit tests (no Docker)
make vet        # Static analysis

# Or directly
cd engine && CGO_ENABLED=1 go build ./...
cd engine && CGO_ENABLED=1 go run ./cmd/engine -addr :8080
```

### Container image

The ZEN library links **dynamically against glibc**. Use `debian-slim` or `distroless` — **not Alpine/musl**.

## Tests

```sh
# Unit tests (fakes only, no Docker)
cd engine && CGO_ENABLED=1 go test ./...

# Integration tests (requires Docker)
cd engine && CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi
```

Integration tests spin up ephemeral Postgres containers and tear them down after.

## Project layout

```
engine/                 Go rules engine (data plane)
  cmd/engine/           Main entry point, graceful shutdown
  internal/
    flow/               Interpreter, context, node handlers
    connect/            Connection registry + resilience
      drivers/          postgres, valkey, rest connectors
    decision/           ZEN engine (cgo) + non-cgo stub
    config/             Config store (in-memory + Postgres)
    webhook/            Signature verification, filtering, mapping
    observ/             Tracing, logging, metrics
    auth/               JWT (data plane) + operator token (admin)
    httpapi/            HTTP handlers, admin API
  migrations/           SQL migrations (embedded)

cms/                    Strapi 5 CMS (control plane / authoring UI)
docs/                   Architecture docs, ADRs, LLDs
```

## Documentation

- `docs/hld.md` — High-level design
- `docs/lld.md` — Low-level design
- `docs/lld-contracts.md` — Node specs and contracts
- `docs/DEPLOYMENT.md` — Deployment guide
- `docs/MONITORING.md` — Observability setup
- `docs/SECRETS.md` — Secret management

## License

MIT
