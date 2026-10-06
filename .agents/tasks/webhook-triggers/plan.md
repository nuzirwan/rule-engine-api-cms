# Implementation Plan: Webhook Triggers (TASK-003)

Build webhook receiver endpoints that trigger flows on incoming webhooks from external services (Stripe, GitHub, Twilio, etc.).

## Integration Points Discovered

### Config Store
- `PgStore` in `engine/internal/config/pgstore.go` implements the `Store` interface with cache-aside pattern
- Objects follow identity table + immutable versions table + `active_pointers` pattern
- Migrations live in `engine/migrations/` with `0001_init.up.sql`, `0002_add_flow_version_note.up.sql`
- Store methods use `pool(env)` lookup, `classifyPg()` for error handling, audit logging

### HTTP API Router
- `NewHandler` in `engine/internal/httpapi/server.go` builds ServeMux with catch-all flow handler
- Admin routes mounted via `admin.mount(mux)` with `a.guard.Protect(fn)` wrapper
- Rate limiting via `rateLimitMiddleware(rl)(mux)` wrapping entire mux
- Error handling via `writeError(w, status, msg)` and `writeJSON(w, status, v)`

### Operator Auth
- `OperatorGuard` in `engine/internal/auth/operator.go` protects admin endpoints
- `RequireRoleFunc` maps routes to required roles
- `OperatorFrom(ctx)` extracts authenticated operator from context

### Flow Triggering
- `genericFlowHandler` resolves active routes per-request via `store.ActiveRoutes`
- Pins flow version via `store.ActiveFlow(ctx, env, method, path)`
- Builds `flow.Ctx` via `flow.NewCtx(reqID, traceID, env, input)`
- Runs interpreter via `interp.Run(ctx, &fv.Tree, version, c, runDeps)`
- Input map comes from path params; webhooks will populate from payload mapping

### Secret Handling
- `SecretProvider` interface in `engine/internal/connect/secret.go`
- `envProvider.Resolve(ctx, ref)` resolves "env:NAME" refs
- `ConnectionDef` has `SecretRef` field (reference, not value)
- Secrets resolved at runtime, never stored or logged

---

## Feature 1: Database Schema and Core Types (FEAT-001)

### What to do
Add webhook database schema (migrations) and core Go types to config package.

### Files to create/modify
- `engine/migrations/0003_webhooks.up.sql` - New migration for webhooks tables
- `engine/migrations/0003_webhooks.down.sql` - Rollback migration
- `engine/internal/config/webhook.go` - Webhook struct and types
- `engine/internal/config/pgstore_webhook.go` - PgStore webhook methods
- `engine/internal/config/store.go` - Extend Store interface (if needed)
- `engine/internal/config/webhook_test.go` - Unit tests

### Data Model
```sql
-- Identity table
CREATE TABLE webhooks (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    provider    text NOT NULL DEFAULT 'generic',  -- stripe|github|generic
    flow_id     text NOT NULL REFERENCES flows(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL
);

-- Immutable versions (append-only)
CREATE TABLE webhook_versions (
    webhook_id  text NOT NULL REFERENCES webhooks(id),
    version     int NOT NULL,
    secret_ref  text NOT NULL,  -- "env:STRIPE_WEBHOOK_SECRET"
    mapping     jsonb NOT NULL, -- {"ctx.eventType":"$.type","ctx.paymentId":"$.data.object.id"}
    filter      jsonb,          -- {"type":["payment_intent.succeeded"]}
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL,
    PRIMARY KEY (webhook_id, version)
);

-- Invocation logs
CREATE TABLE webhook_logs (
    id              bigserial PRIMARY KEY,
    webhook_id      text NOT NULL,
    at              timestamptz NOT NULL DEFAULT now(),
    provider        text NOT NULL,
    event_type      text,
    flow_triggered  text,
    status          text NOT NULL,  -- success|filtered|signature_failed|error
    payload_hash    text,           -- sha256 of raw payload (for dedup/debug)
    error           text
);
CREATE INDEX idx_webhook_logs_id_at ON webhook_logs (webhook_id, at DESC);
```

### Verify
`cd /home/nuzirwan/project/rule-engine-api/.worktrees/webhook-triggers/engine && CGO_ENABLED=1 go build ./... && go test ./internal/config/...`

---

## Feature 2: Webhook Receiver with Signature Verification (FEAT-002)

### What to do
Implement POST /webhooks/{webhook_id} receiver endpoint with provider-specific signature verification and payload mapping.

### Files to create/modify
- `engine/internal/webhook/provider.go` - Provider interface and types
- `engine/internal/webhook/stripe.go` - Stripe signature verification
- `engine/internal/webhook/github.go` - GitHub signature verification
- `engine/internal/webhook/generic.go` - Generic HMAC verification
- `engine/internal/webhook/mapping.go` - JSONPath payload mapping
- `engine/internal/webhook/filter.go` - Event filtering
- `engine/internal/httpapi/webhook.go` - Receiver handler
- `engine/internal/httpapi/server.go` - Mount webhook endpoint
- `engine/internal/webhook/*_test.go` - Unit tests
- `engine/internal/httpapi/webhook_test.go` - Handler tests

### Signature Verification Design
```go
type Provider interface {
    // VerifySignature validates the webhook signature
    VerifySignature(payload []byte, signature string, secret []byte) error
    // ParseEvent extracts provider-specific event data
    ParseEvent(payload []byte, headers http.Header) (*WebhookEvent, error)
}

type WebhookEvent struct {
    Type    string
    Payload map[string]any
    Headers http.Header
    RawBody []byte
}
```

**Stripe**: `Stripe-Signature: t=timestamp,v1=hmac_hex` - compute HMAC-SHA256 of `timestamp.payload`
**GitHub**: `X-Hub-Signature-256: sha256=hmac_hex` - compute HMAC-SHA256 of payload
**Generic**: Configurable header + algorithm

### Verify
`cd /home/nuzirwan/project/rule-engine-api/.worktrees/webhook-triggers/engine && CGO_ENABLED=1 go build ./... && go test ./internal/webhook/... && go test ./internal/httpapi/...`

---

## Feature 3: Webhook Admin API (FEAT-003)

### What to do
Implement CRUD endpoints for webhook management under /admin/webhooks, protected by operator auth.

### Files to create/modify
- `engine/internal/httpapi/webhook_admin.go` - Admin handlers
- `engine/internal/httpapi/admin.go` - Mount webhook admin routes, update RBAC
- `engine/internal/httpapi/webhook_admin_test.go` - Handler tests

### Admin Endpoints
| Method | Path | Description | Role |
|--------|------|-------------|------|
| GET | /admin/webhooks | List all webhooks | webhook.read |
| GET | /admin/webhooks/{id} | Get webhook by ID | webhook.read |
| POST | /admin/webhooks | Create webhook | webhook.write |
| PUT | /admin/webhooks/{id} | Update webhook | webhook.write |
| DELETE | /admin/webhooks/{id} | Delete webhook | webhook.write |
| GET | /admin/webhooks/{id}/logs | Get invocation logs | webhook.read |

### Verify
`cd /home/nuzirwan/project/rule-engine-api/.worktrees/webhook-triggers/engine && CGO_ENABLED=1 go build ./... && go test ./internal/httpapi/...`

---

## Feature 4: CMS Webhook Integration (FEAT-004)

### What to do
Add webhook content type to Strapi CMS and extend AdminClient with webhook methods.

### Files to create/modify
- `cms/src/api/webhook/content-types/webhook/schema.json` - Content type schema
- `cms/src/api/webhook/controllers/webhook.ts` - Controller (minimal)
- `cms/src/api/webhook/routes/webhook.ts` - Routes (minimal)
- `cms/src/api/webhook/services/webhook.ts` - Service (minimal)
- `cms/src/components/webhook/mapping-field.json` - Mapping component
- `cms/src/components/webhook/filter-field.json` - Filter component
- `cms/types/engine.ts` - Add webhook types
- `cms/src/plugins/rule-engine/server/src/services/admin-client.ts` - Add webhook methods

### Content Type Schema
```json
{
  "kind": "collectionType",
  "collectionName": "webhooks",
  "info": {
    "singularName": "webhook",
    "pluralName": "webhooks",
    "displayName": "Webhook"
  },
  "attributes": {
    "webhookId": { "type": "uid", "required": true },
    "name": { "type": "string", "required": true },
    "secretRef": { "type": "string", "maxLength": 256 },
    "provider": { "type": "enumeration", "enum": ["stripe", "github", "generic"], "default": "generic" },
    "flowId": { "type": "relation", "relation": "manyToOne", "target": "api::flow.flow" },
    "mapping": { "type": "json" },
    "filter": { "type": "json" },
    "environment": { "type": "relation", "relation": "manyToOne", "target": "api::environment.environment" },
    "engineVersion": { "type": "integer" }
  }
}
```

### Verify
`cd /home/nuzirwan/project/rule-engine-api/.worktrees/webhook-triggers/cms && npm run build && npm run test`

---

## Test Plan

### Unit Tests
1. **Signature Verification**: Test Stripe, GitHub, Generic HMAC verification with valid/invalid signatures
2. **Payload Mapping**: Test JSONPath extraction with various payloads
3. **Event Filtering**: Test filter matching logic
4. **Store Methods**: Test webhook CRUD operations
5. **Admin Handlers**: Test HTTP layer with fake store

### Integration Tests
1. Full webhook flow: POST to /webhooks/{id} -> signature verify -> filter -> trigger flow
2. Admin CRUD: Create webhook -> Update -> Query logs -> Delete
3. Error cases: Invalid signature (401), Unknown webhook (404), Filter reject (200 no trigger)

---

## Constraints Verification Checklist
- [ ] Secrets via secretRef only (never inline in webhook config)
- [ ] Operator auth on all /admin/webhooks endpoints
- [ ] Rate limiting on /webhooks/{id} endpoint
- [ ] Signature verification required in production
- [ ] Webhook logs redact sensitive payload fields
- [ ] Audit trail for webhook config changes
