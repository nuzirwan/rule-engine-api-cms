# TASK-003: Webhook Triggers

## Summary
Add webhook receiver endpoints that trigger flows on incoming webhooks from external services (Stripe, GitHub, Twilio, etc.). Currently flows are only triggered by direct HTTP requests to their configured paths.

## Context
- Current trigger: HTTP request to flow's `method`/`path` (e.g., `GET /order/{id}`)
- Webhooks are different: external service POSTs to a fixed URL, payload varies by service
- Need: generic webhook receiver that routes to flows based on config

## Requirements

### 1. Webhook Endpoint
```
POST /webhooks/{webhook_id}
```

- Receives any JSON payload
- Looks up webhook config by `webhook_id`
- Extracts relevant data from payload
- Triggers configured flow with extracted data as context

### 2. Webhook Configuration (new content type)
```json
{
  "id": "stripe-payment",
  "name": "Stripe Payment Webhook",
  "secret": "whsec_...",           // for signature verification
  "secretRef": "stripe-webhook-secret", // or reference to secret store
  "provider": "stripe",             // optional: enables provider-specific parsing
  "flowId": "process-payment",      // flow to trigger
  "mapping": {                      // map webhook payload to flow context
    "ctx.eventType": "$.type",
    "ctx.paymentId": "$.data.object.id",
    "ctx.amount": "$.data.object.amount"
  },
  "filter": {                       // optional: only trigger on certain events
    "type": ["payment_intent.succeeded", "payment_intent.failed"]
  },
  "env": ""
}
```

### 3. Signature Verification
Support common webhook signature schemes:
- **Stripe**: `Stripe-Signature` header, HMAC-SHA256
- **GitHub**: `X-Hub-Signature-256` header, HMAC-SHA256
- **Generic HMAC**: configurable header + algorithm
- **None**: for testing/internal webhooks

### 4. Provider-Specific Parsing (optional)
Built-in parsers for common providers:
- Stripe: unwrap event envelope, handle idempotency
- GitHub: parse event type from `X-GitHub-Event` header
- Generic: use JSONPath mapping directly

### 5. Webhook Admin API
```
GET  /admin/webhooks           — list all webhooks
GET  /admin/webhooks/{id}      — get webhook config
POST /admin/webhooks           — create webhook
PUT  /admin/webhooks/{id}      — update webhook
DELETE /admin/webhooks/{id}    — delete webhook
```

### 6. Webhook Logging
- Log all incoming webhooks (success + failure)
- Store: timestamp, webhook_id, provider, event_type, flow_triggered, status
- Queryable via admin API: `GET /admin/webhooks/{id}/logs`

## Files to Modify/Create

### Engine (`engine/`)

#### Config Layer
- `internal/config/webhook.go` — Webhook struct, store methods
- `internal/config/pg_store_webhook.go` — Postgres implementation
- `internal/config/migrations/004_webhooks.sql` — webhook tables

#### HTTP Layer
- `internal/httpapi/webhook.go` — webhook receiver handler
- `internal/httpapi/webhook_admin.go` — admin CRUD handlers
- `internal/httpapi/webhook_verify.go` — signature verification

#### Webhook Processing
- `internal/webhook/provider.go` — provider interface
- `internal/webhook/stripe.go` — Stripe provider
- `internal/webhook/github.go` — GitHub provider
- `internal/webhook/generic.go` — generic HMAC provider

### CMS (`cms/src/plugins/rule-engine/`)
- `server/content-types/webhook/schema.json` — Webhook content type
- `admin/src/pages/WebhooksPage.tsx` — webhook management UI
- `admin/src/components/WebhookEditor/index.tsx` — webhook editor

## Acceptance Criteria
- [ ] `POST /webhooks/{id}` receives and processes webhooks
- [ ] Signature verification works for Stripe and GitHub
- [ ] Payload mapping extracts data into flow context
- [ ] Event filtering skips non-matching events
- [ ] Admin CRUD endpoints work
- [ ] Webhook logs are queryable
- [ ] CMS can create/edit webhooks

## Testing
- Unit tests for signature verification (Stripe, GitHub, generic)
- Unit tests for payload mapping (JSONPath extraction)
- Unit tests for event filtering
- Integration test: mock Stripe webhook → flow triggered
- Integration test: invalid signature → 401

## Security Considerations
- Secrets stored via `secretRef`, never in config directly
- Signature verification required in production (can be disabled for testing)
- Rate limiting on webhook endpoint (reuse existing rate limiter)
- Webhook logs redact sensitive payload fields

## Example: Stripe Integration

1. Create webhook in CMS:
   - ID: `stripe-payments`
   - Provider: Stripe
   - Secret Ref: `stripe-webhook-secret`
   - Flow: `process-payment`
   - Filter: `payment_intent.succeeded`

2. Configure Stripe dashboard:
   - Endpoint URL: `https://api.example.com/webhooks/stripe-payments`
   - Events: `payment_intent.succeeded`

3. When payment succeeds:
   - Stripe POSTs to `/webhooks/stripe-payments`
   - Engine verifies signature
   - Engine extracts payment data
   - Engine triggers `process-payment` flow
   - Flow processes payment (update DB, send email, etc.)
