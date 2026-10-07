# Resilience Patterns Reference — fault tolerance configuration

The rule engine provides built-in resilience patterns to handle transient failures,
prevent cascade failures, and ensure reliable operation. This guide covers circuit
breakers, retries, timeouts, rate limiting, idempotency, and saga compensation.

**Related architecture:** [slice-b-connections.md](../architecture/lld/slice-b-connections.md)

## Execution Order

Resilience wrappers execute in this order:

```
timeout → circuit breaker → retry → actual operation
```

1. **Timeout** (outermost): bounds the entire operation including all retries
2. **Circuit Breaker**: per-connection-key, short-circuits when open
3. **Retry**: exponential backoff for transient failures
4. **Operation**: the actual database/HTTP/cache call

---

## Connection-Level Resilience

Define resilience policies at the connection level:

```json
{
  "key": "pg-main",
  "type": "postgres",
  "settings": { /* ... */ },
  "resilience": {
    "timeout": "5s",
    "retry": {
      "maxAttempts": 3,
      "baseBackoff": "100ms",
      "maxBackoff": "2s"
    },
    "breaker": {
      "failureThreshold": 5,
      "failureRatio": 0.0,
      "openTimeout": "10s"
    }
  }
}
```

---

## Timeout

Bounds the total time for an operation, including retries.

```json
{
  "resilience": {
    "timeout": "5s"
  }
}
```

| Setting   | Type     | Description                           |
|-----------|----------|---------------------------------------|
| `timeout` | duration | Maximum time for the entire operation |

If the timeout expires, the operation fails immediately regardless of retry state.

---

## Retry with Exponential Backoff

Retries failed operations with increasing delays.

```json
{
  "resilience": {
    "retry": {
      "maxAttempts": 3,
      "baseBackoff": "100ms",
      "maxBackoff": "2s"
    }
  }
}
```

| Setting       | Type     | Default | Description                    |
|---------------|----------|---------|--------------------------------|
| `maxAttempts` | int      | 3       | Total attempts (1 = no retry)  |
| `baseBackoff` | duration | 100ms   | Initial backoff duration       |
| `maxBackoff`  | duration | 2s      | Maximum backoff duration       |

### Backoff Calculation

```
delay = min(baseBackoff * 2^(attempt-1), maxBackoff)
```

Example with `baseBackoff: 100ms`, `maxBackoff: 2s`:
- Attempt 1: immediate
- Attempt 2: wait 100ms
- Attempt 3: wait 200ms
- Attempt 4: wait 400ms
- ...
- Capped at 2s

### Retryable vs Non-Retryable Errors

Only transient errors are retried:
- Network timeouts
- Connection refused
- 5xx HTTP responses

Non-retryable errors fail immediately:
- 4xx HTTP responses (client errors)
- Validation errors
- Business logic errors

---

## Circuit Breaker

Prevents cascade failures by "opening" when error rates exceed thresholds.

```json
{
  "resilience": {
    "breaker": {
      "failureThreshold": 5,
      "failureRatio": 0.0,
      "openTimeout": "10s"
    }
  }
}
```

| Setting            | Type    | Default | Description                              |
|--------------------|---------|---------|------------------------------------------|
| `failureThreshold` | uint32  | 5       | Consecutive failures before opening      |
| `failureRatio`     | float64 | 0.0     | Failure ratio threshold (0 = count only) |
| `openTimeout`      | duration| 10s     | Time before allowing test request        |

### Circuit States

```
CLOSED → (threshold exceeded) → OPEN → (timeout) → HALF-OPEN
   ↑                                                    ↓
   └──────────────── (success) ─────────────────────────┘
                          │
                      (failure) → OPEN
```

- **CLOSED**: Normal operation, tracking failures
- **OPEN**: All requests fail immediately with circuit breaker error
- **HALF-OPEN**: Allow one test request; success closes, failure reopens

### Default Breaker Settings

| Setting             | Default |
|---------------------|---------|
| `failureThreshold`  | 5       |
| `openTimeout`       | 10s     |
| `halfOpenMaxRequests` | 1     |
| `minRequests`       | 3       |

---

## Per-Action Override

Override connection-level resilience for specific actions:

```json
{
  "id": "long-running-query",
  "type": "action",
  "spec": {
    "connection": "pg-main",
    "operation": {
      "kind": "query",
      "payload": { "sql": "SELECT * FROM large_table" }
    },
    "resilience": {
      "timeoutMs": 30000,
      "retries": 5,
      "breakerOff": true
    }
  }
}
```

| Field       | Type | Description                              |
|-------------|------|------------------------------------------|
| `timeoutMs` | int  | Override timeout in milliseconds         |
| `retries`   | int  | Override max retry attempts              |
| `breakerOff`| bool | Disable circuit breaker for this action  |

---

## Rate Limiting

Protects endpoints from excessive traffic.

```json
{
  "rateLimit": {
    "rps": 100.0,
    "burst": 150,
    "byToken": false
  }
}
```

| Setting   | Type    | Description                              |
|-----------|---------|------------------------------------------|
| `rps`     | float64 | Requests per second limit                |
| `burst`   | int     | Token bucket capacity (burst allowance)  |
| `byToken` | bool    | Key by bearer token (true) or IP (false) |

### Rate Limit Response

When rate limited, the API returns:

```
HTTP/1.1 429 Too Many Requests
Retry-After: 1
Content-Type: application/json

{
  "error": "rate limit exceeded",
  "retry_after": 1
}
```

---

## Idempotency Key Deduplication

Prevents duplicate processing of the same request.

```json
{
  "id": "create-order",
  "type": "action",
  "spec": {
    "connection": "pg-main",
    "operation": { /* ... */ },
    "idempotencyKeyFrom": "{{input.request_id}}"
  }
}
```

| Field                | Type   | Description                      |
|----------------------|--------|----------------------------------|
| `idempotencyKeyFrom` | string | Template for idempotency key     |

### Dedup Semantics

1. **First writer wins**: Acquires lock with `SET NX PX` (set if not exists, with expiry)
2. **On write failure**: Lock is released, allowing retry
3. **On write success**: Lock held until TTL (default 30s)
4. **Duplicate within window**: Returns `{"deduped": true}` without re-executing

### Example Flow with Idempotency

```json
{
  "id": "create-payment",
  "type": "action",
  "spec": {
    "connection": "payment-api",
    "operation": {
      "kind": "http",
      "payload": {
        "method": "POST",
        "path": "/v1/charges",
        "body": { "amount": "{{input.amount}}" }
      }
    },
    "idempotencyKeyFrom": "{{input.order_id}}-payment",
    "saveAs": "paymentResult"
  }
}
```

If the same `order_id` triggers this action within the dedup window, the second
request returns immediately with `{"deduped": true}`.

---

## Saga Compensation

Handles distributed transactions with compensation logic for rollback.

### Saga States

```
started → executing → committed
              ↓
         compensating → compensated
              ↓
           failed (manual intervention)
```

| State         | Description                                  |
|---------------|----------------------------------------------|
| `started`     | Saga initialized                             |
| `executing`   | Steps in progress                            |
| `committed`   | All steps completed successfully             |
| `compensating`| Rolling back completed steps                 |
| `compensated` | Rollback completed                           |
| `failed`      | Compensation failed, needs manual review     |

### Compensation Spec

Define compensation operations for reversible actions:

```json
{
  "id": "reserve-inventory",
  "type": "action",
  "spec": {
    "connection": "inventory-api",
    "operation": {
      "kind": "http",
      "payload": {
        "method": "POST",
        "path": "/v1/reservations",
        "body": { "sku": "{{item.sku}}", "qty": "{{item.quantity}}" }
      }
    },
    "compensation": {
      "connection": "inventory-api",
      "operation": {
        "kind": "http",
        "payload": {
          "method": "DELETE",
          "path": "/v1/reservations/{{data.reservation_id}}"
        }
      }
    },
    "saveAs": "reservation"
  }
}
```

If a later step fails, the compensation operation executes to release the reservation.

---

## OnError Modes

Control flow behavior when actions fail:

```json
{
  "id": "notify-failure",
  "type": "action",
  "spec": {
    "connection": "notification-api",
    "operation": { /* ... */ },
    "onError": "continue"
  }
}
```

| Mode       | Behavior                                      |
|------------|-----------------------------------------------|
| `"fail"`   | (default) Abort flow walk on failure          |
| `"continue"` | Record failure, continue with next step     |

Use `"continue"` for non-critical operations like notifications or logging.

---

## Complete Example

A resilient order processing flow:

```json
{
  "id": "process-order",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": {
      "method": "POST",
      "path": "/orders",
      "input": { "body": true, "headers": ["X-Idempotency-Key"] }
    },
    "children": [
      {
        "id": "seq-main",
        "type": "sequence",
        "spec": { "stopOnError": true },
        "children": [
          {
            "id": "check-inventory",
            "type": "action",
            "spec": {
              "connection": "inventory-api",
              "operation": {
                "kind": "http",
                "payload": {
                  "method": "POST",
                  "path": "/v1/check",
                  "body": { "items": "{{input.items}}" }
                }
              },
              "saveAs": "inventoryCheck",
              "resilience": {
                "timeoutMs": 3000,
                "retries": 2
              }
            }
          },
          {
            "id": "create-order",
            "type": "action",
            "spec": {
              "connection": "pg-main",
              "operation": {
                "kind": "exec",
                "payload": {
                  "sql": "INSERT INTO orders (customer_id, items, total) VALUES ($1, $2, $3) RETURNING id",
                  "params": [
                    { "value": "{{input.customer_id}}", "as": "int" },
                    { "value": "{{input.items}}", "as": "text" },
                    { "value": "{{input.total}}", "as": "numeric" }
                  ]
                }
              },
              "saveAs": "order",
              "idempotencyKeyFrom": "{{input.X-Idempotency-Key}}",
              "resilience": {
                "timeoutMs": 5000,
                "retries": 3
              }
            }
          },
          {
            "id": "process-payment",
            "type": "action",
            "spec": {
              "connection": "payment-api",
              "operation": {
                "kind": "http",
                "payload": {
                  "method": "POST",
                  "path": "/v1/charges",
                  "body": {
                    "amount": "{{input.total}}",
                    "order_id": "{{order.id}}"
                  }
                }
              },
              "saveAs": "payment",
              "compensation": {
                "connection": "payment-api",
                "operation": {
                  "kind": "http",
                  "payload": {
                    "method": "POST",
                    "path": "/v1/refunds",
                    "body": { "charge_id": "{{payment.id}}" }
                  }
                }
              },
              "resilience": {
                "timeoutMs": 10000,
                "retries": 2,
                "breakerOff": false
              }
            }
          },
          {
            "id": "send-notification",
            "type": "action",
            "spec": {
              "connection": "notification-api",
              "operation": {
                "kind": "http",
                "payload": {
                  "method": "POST",
                  "path": "/v1/send",
                  "body": {
                    "type": "order_confirmation",
                    "order_id": "{{order.id}}"
                  }
                }
              },
              "onError": "continue"
            }
          }
        ]
      }
    ]
  }
}
```

This flow demonstrates:
- **Idempotency**: Order creation uses `X-Idempotency-Key` for dedup
- **Per-action timeouts**: Different timeout for payment vs inventory check
- **Compensation**: Payment has a refund compensation for rollback
- **OnError continue**: Notification failure doesn't abort the order

---

## Best Practices

1. **Set realistic timeouts** — Match timeouts to actual operation duration plus margin for retries.

2. **Tune circuit breaker thresholds** — Start conservative (high threshold) and tighten based on observed error patterns.

3. **Use idempotency for writes** — Any operation that creates or modifies data should have an idempotency key.

4. **Plan compensation operations** — For distributed transactions, define how to undo each step.

5. **Monitor breaker state** — Watch metrics for circuit breaker opens; frequent opens indicate upstream issues.

6. **Test failure scenarios** — Use chaos testing to verify resilience behavior under load and failures.
