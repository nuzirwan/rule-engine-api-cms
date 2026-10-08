# Webhooks Reference — provider configuration and signature verification

The rule engine supports receiving webhooks from external providers with automatic
signature verification and event routing. This guide covers Stripe, GitHub, and
generic webhook configuration.

**Related architecture:** [slice-h-webhook.md](../architecture/lld/slice-h-webhook.md)

## Overview

Webhooks flow through this pipeline:

```
Incoming Request → Signature Verification → Event Parsing → Filter → Flow Trigger
```

Each webhook configuration specifies:
- A provider for signature verification
- A flow to trigger on valid events
- Optional filtering by event type
- Field mapping for flow input

## Webhook Endpoint

All webhooks are received at:

```
POST /webhooks/{webhook_id}
```

The `webhook_id` matches the `id` field in webhook configuration.

---

## Supported Providers

| Provider  | Signature Header        | Algorithm    |
|-----------|-------------------------|--------------|
| `stripe`  | `Stripe-Signature`      | HMAC-SHA256  |
| `github`  | `X-Hub-Signature-256`   | HMAC-SHA256  |
| `generic` | `X-Webhook-Signature`   | HMAC-SHA256/SHA1 |

---

## Webhook Configuration

```json
{
  "id": "stripe-payments",
  "name": "Stripe Payment Events",
  "secretRef": "env:STRIPE_WEBHOOK_SECRET",
  "provider": "stripe",
  "flowId": "process-payment",
  "mapping": {
    "order_id": "$.data.object.metadata.order_id",
    "amount": "$.data.object.amount",
    "currency": "$.data.object.currency"
  },
  "filter": {
    "event_type": ["payment_intent.succeeded", "payment_intent.payment_failed"]
  },
  "env": "production",
  "version": 1
}
```

| Field       | Type            | Description                                |
|-------------|-----------------|--------------------------------------------|
| `id`        | string          | Unique webhook identifier (URL path)       |
| `name`      | string          | Human-readable name                        |
| `secretRef` | string          | Secret reference for signature verification|
| `provider`  | string          | `stripe`, `github`, or `generic`           |
| `flowId`    | string          | Flow to trigger on valid events            |
| `mapping`   | map[string]string | JSONPath mapping for flow input          |
| `filter`    | object          | Event type filtering                       |
| `env`       | string          | Environment (development, staging, production) |
| `version`   | int             | Configuration version                      |

---

## Stripe Webhook

Receives payment events from Stripe.

### Configuration

```json
{
  "id": "stripe-payments",
  "name": "Stripe Payment Events",
  "secretRef": "env:STRIPE_WEBHOOK_SECRET",
  "provider": "stripe",
  "flowId": "handle-stripe-payment",
  "mapping": {
    "payment_intent_id": "$.data.object.id",
    "order_id": "$.data.object.metadata.order_id",
    "amount": "$.data.object.amount",
    "status": "$.data.object.status"
  },
  "filter": {
    "event_type": [
      "payment_intent.succeeded",
      "payment_intent.payment_failed",
      "charge.refunded"
    ]
  },
  "env": "production"
}
```

### Incoming Request Example

```sh
curl -X POST http://localhost:8080/webhooks/stripe-payments \
  -H "Content-Type: application/json" \
  -H "Stripe-Signature: t=1234567890,v1=abc123..." \
  -d '{
    "id": "evt_123",
    "type": "payment_intent.succeeded",
    "data": {
      "object": {
        "id": "pi_123",
        "amount": 2000,
        "currency": "usd",
        "status": "succeeded",
        "metadata": {
          "order_id": "ORD-456"
        }
      }
    }
  }'
```

### Response

Success:
```json
{
  "status": "accepted",
  "trace_id": "abc123"
}
```

Filtered (event not in filter list):
```json
{
  "status": "filtered",
  "reason": "event type not in filter"
}
```

### Stripe Signature Format

```
Stripe-Signature: t=1234567890,v1=<signature>
```

- `t`: Unix timestamp of when Stripe sent the event
- `v1`: HMAC-SHA256 signature of `{timestamp}.{payload}`

---

## GitHub Webhook

Receives repository events from GitHub.

### Configuration

```json
{
  "id": "github-deploy",
  "name": "GitHub Deployment Events",
  "secretRef": "env:GITHUB_WEBHOOK_SECRET",
  "provider": "github",
  "flowId": "handle-github-push",
  "mapping": {
    "repository": "$.repository.full_name",
    "branch": "$.ref",
    "commit": "$.after",
    "pusher": "$.pusher.name"
  },
  "filter": {
    "event_type": ["push", "pull_request"]
  },
  "env": "production"
}
```

### Incoming Request Example

```sh
curl -X POST http://localhost:8080/webhooks/github-deploy \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: push" \
  -H "X-Hub-Signature-256: sha256=abc123..." \
  -d '{
    "ref": "refs/heads/main",
    "after": "abc123def456",
    "repository": {
      "full_name": "org/repo"
    },
    "pusher": {
      "name": "developer"
    }
  }'
```

### GitHub Event Types

Common event types to filter on:
- `push` — Code pushed to repository
- `pull_request` — PR opened, closed, merged
- `release` — Release published
- `issues` — Issue created, updated
- `workflow_run` — GitHub Actions workflow completed

### GitHub Signature Format

```
X-Hub-Signature-256: sha256=<signature>
```

HMAC-SHA256 of the raw request body.

---

## Generic Webhook

For custom webhook sources with configurable signature verification.

### Configuration

```json
{
  "id": "custom-webhook",
  "name": "Custom Service Events",
  "secretRef": "env:CUSTOM_WEBHOOK_SECRET",
  "provider": "generic",
  "flowId": "handle-custom-event",
  "mapping": {
    "event_id": "$.id",
    "event_type": "$.type",
    "data": "$.payload"
  },
  "filter": {
    "event_type": ["order.created", "order.updated"]
  },
  "env": "production"
}
```

### Supported Signature Formats

The generic provider accepts multiple signature formats:

1. **Raw hex**: Just the hex-encoded signature
   ```
   X-Webhook-Signature: abc123def456...
   ```

2. **Prefixed with algorithm**:
   ```
   X-Webhook-Signature: sha256=abc123def456...
   X-Webhook-Signature: sha1=abc123def456...
   ```

Default header: `X-Webhook-Signature`
Default algorithm: SHA256

### Custom Header

If your provider uses a different header, the generic provider checks these in order:
1. `X-Webhook-Signature`
2. `X-Signature`
3. `Signature`

---

## Webhook Event Structure

After verification, the webhook handler creates a `WebhookEvent`:

```json
{
  "type": "payment_intent.succeeded",
  "payload": { /* deserialized JSON body */ },
  "headers": {
    "Content-Type": "application/json",
    "Stripe-Signature": "t=123..."
  },
  "rawBody": "<raw bytes>"
}
```

The `payload` is mapped according to the webhook's `mapping` config and passed
to the flow as input.

---

## Webhook Log Status

Each webhook invocation is logged with a status:

| Status     | Description                                |
|------------|--------------------------------------------|
| `accepted` | Signature valid, flow triggered            |
| `filtered` | Valid but event type not in filter list    |
| `rejected` | Signature verification failed              |
| `error`    | Processing error (e.g., flow execution)    |

---

## Field Mapping

The `mapping` config extracts fields from the webhook payload using JSONPath:

```json
{
  "mapping": {
    "target_field": "$.path.to.source.field"
  }
}
```

These mapped fields become the flow's input:

```json
// Webhook payload
{
  "data": {
    "object": {
      "id": "pi_123",
      "amount": 2000
    }
  }
}

// Mapping
{
  "payment_id": "$.data.object.id",
  "amount": "$.data.object.amount"
}

// Flow input
{
  "payment_id": "pi_123",
  "amount": 2000
}
```

---

## Complete Example: Payment Processing

Webhook configuration:

```json
{
  "id": "stripe-payments",
  "name": "Stripe Payment Webhooks",
  "secretRef": "env:STRIPE_WEBHOOK_SECRET",
  "provider": "stripe",
  "flowId": "process-stripe-payment",
  "mapping": {
    "payment_id": "$.data.object.id",
    "order_id": "$.data.object.metadata.order_id",
    "amount": "$.data.object.amount",
    "status": "$.data.object.status",
    "customer_email": "$.data.object.receipt_email"
  },
  "filter": {
    "event_type": ["payment_intent.succeeded", "payment_intent.payment_failed"]
  },
  "env": "production"
}
```

Flow definition:

```json
{
  "id": "process-stripe-payment",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": {
      "method": "POST",
      "path": "/internal/payment-callback",
      "input": { "body": true }
    },
    "children": [
      {
        "id": "seq-main",
        "type": "sequence",
        "spec": { "stopOnError": true },
        "children": [
          {
            "id": "log-payment",
            "type": "logger",
            "spec": {
              "label": "stripe-payment-received",
              "level": "info",
              "capture": ["input.payment_id", "input.order_id", "input.status"]
            }
          },
          {
            "id": "check-status",
            "type": "condition",
            "spec": {
              "jdmId": "payment-status-check",
              "input": ["status"],
              "trueKey": "handle-success",
              "falseKey": "handle-failure"
            },
            "children": [
              {
                "id": "handle-success",
                "type": "sequence",
                "spec": {},
                "children": [
                  {
                    "id": "update-order-paid",
                    "type": "action",
                    "spec": {
                      "connection": "pg-main",
                      "operation": {
                        "kind": "exec",
                        "payload": {
                          "sql": "UPDATE orders SET payment_status = 'PAID' WHERE id = $1",
                          "params": ["{{input.order_id}}"]
                        }
                      }
                    }
                  }
                ]
              },
              {
                "id": "handle-failure",
                "type": "sequence",
                "spec": {},
                "children": [
                  {
                    "id": "update-order-failed",
                    "type": "action",
                    "spec": {
                      "connection": "pg-main",
                      "operation": {
                        "kind": "exec",
                        "payload": {
                          "sql": "UPDATE orders SET payment_status = 'FAILED' WHERE id = $1",
                          "params": ["{{input.order_id}}"]
                        }
                      }
                    }
                  }
                ]
              }
            ]
          }
        ]
      }
    ]
  }
}
```

---

## Best Practices

1. **Verify signatures** — Always use signature verification; never disable it in production.

2. **Use specific filters** — Only subscribe to event types you need to process.

3. **Idempotent handlers** — Webhooks may be delivered multiple times; make handlers idempotent.

4. **Return quickly** — Acknowledge webhooks promptly; process asynchronously if needed.

5. **Log all events** — Keep audit logs of all webhook invocations for debugging.

6. **Rotate secrets** — Periodically rotate webhook secrets and update configurations.

7. **Test with dry-run** — Validate webhook handling using dry-run mode before production.
