# Flow Nodes Reference — complete node types and configuration

The rule engine flow interpreter executes a tree of typed nodes. Each node has an
`id`, `type`, `spec` (type-specific configuration), and optional `children`. This
guide covers all 16 node types with concrete config examples.

**Related architecture:** [slice-a-interpreter.md](../architecture/lld/slice-a-interpreter.md)

## Node Structure

Every node follows this shape:

```json
{
  "id": "unique-node-id",
  "type": "trigger",
  "spec": { /* type-specific configuration */ },
  "children": [ /* nested nodes for control types */ ]
}
```

## Node Types Overview

| Type              | Category | Purpose                                    |
|-------------------|----------|--------------------------------------------|
| `trigger`         | control  | HTTP endpoint entry point                  |
| `messageTrigger`  | control  | Message queue entry point (Kafka)          |
| `sequence`        | control  | Execute children in order                  |
| `parallel`        | control  | Execute children concurrently              |
| `condition`       | control  | Binary branching via ZEN decision          |
| `switch`          | control  | Multi-branch routing via ZEN decision      |
| `forEach`         | control  | Iterate over array                         |
| `action`          | leaf     | Execute connection operation               |
| `decision`        | leaf     | Evaluate ZEN decision table                |
| `set`             | leaf     | Set value in context                       |
| `logger`          | leaf     | Structured logging                         |
| `response`        | leaf     | Set HTTP response                          |
| `filter`          | leaf     | Filter array by ZEN predicate              |
| `find`            | leaf     | Find first matching item                   |
| `map`             | leaf     | Transform array items                      |
| `reduce`          | leaf     | Aggregate array to single value            |

---

## Control Nodes

### trigger — HTTP Entry Point

Defines an HTTP endpoint that activates this flow.

```json
{
  "id": "order-trigger",
  "type": "trigger",
  "spec": {
    "method": "POST",
    "path": "/orders/{order_id}",
    "input": {
      "params": ["order_id"],
      "query": ["include_details"],
      "headers": ["X-Request-ID"],
      "body": true,
      "schema": {
        "type": "object",
        "required": ["amount"],
        "properties": {
          "amount": { "type": "number", "minimum": 0 }
        }
      }
    }
  },
  "children": [ /* flow nodes */ ]
}
```

| Field               | Type     | Description                                         |
|---------------------|----------|-----------------------------------------------------|
| `method`            | string   | HTTP method: `GET`, `POST`, `PUT`, `DELETE`         |
| `path`              | string   | URL path with `{param}` placeholders                |
| `input.params`      | []string | Path parameters to extract                          |
| `input.query`       | []string | Query parameters to extract                         |
| `input.headers`     | []string | Headers to extract (case-insensitive)               |
| `input.body`        | bool     | Whether to parse JSON body                          |
| `input.schema`      | object   | JSON Schema for body validation                     |

### messageTrigger — Kafka Entry Point

Defines a message queue consumer that activates this flow.

```json
{
  "id": "order-consumer",
  "type": "messageTrigger",
  "spec": {
    "connectionKey": "kafka-orders",
    "topic": "orders.created",
    "flowId": "process-order",
    "deadLetterTopic": "orders.dlq"
  },
  "children": [ /* flow nodes */ ]
}
```

| Field             | Type   | Description                             |
|-------------------|--------|-----------------------------------------|
| `connectionKey`   | string | Connection registry key for Kafka       |
| `topic`           | string | Topic to consume from                   |
| `flowId`          | string | Flow identifier for tracing             |
| `deadLetterTopic` | string | Topic for failed messages               |

### sequence — Sequential Execution

Executes children one after another. Stops on first error by default.

```json
{
  "id": "order-steps",
  "type": "sequence",
  "spec": {
    "stopOnError": true
  },
  "children": [
    { "id": "validate", "type": "action", "spec": { /* ... */ } },
    { "id": "save", "type": "action", "spec": { /* ... */ } },
    { "id": "notify", "type": "action", "spec": { /* ... */ } }
  ]
}
```

| Field         | Type | Default | Description                           |
|---------------|------|---------|---------------------------------------|
| `stopOnError` | bool | `true`  | Abort on child failure                |

### parallel — Concurrent Execution

Executes children concurrently with optional concurrency limit.

```json
{
  "id": "fetch-all",
  "type": "parallel",
  "spec": {
    "maxConcurrency": 5,
    "failFast": true
  },
  "children": [
    { "id": "fetch-user", "type": "action", "spec": { /* ... */ } },
    { "id": "fetch-orders", "type": "action", "spec": { /* ... */ } },
    { "id": "fetch-inventory", "type": "action", "spec": { /* ... */ } }
  ]
}
```

| Field            | Type | Default | Description                              |
|------------------|------|---------|------------------------------------------|
| `maxConcurrency` | int  | 0       | Max parallel executions (0 = unlimited)  |
| `failFast`       | bool | `true`  | Cancel remaining on first failure        |

### condition — Binary Branching

Routes to one of two branches based on a ZEN decision result.

```json
{
  "id": "payment-check",
  "type": "condition",
  "spec": {
    "jdmId": "payment-status-check",
    "input": ["data.payment_status", "data.amount"],
    "trueKey": "process-payment",
    "falseKey": "await-payment",
    "branchField": "approved"
  },
  "children": [
    { "id": "process-payment", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "await-payment", "type": "sequence", "spec": {}, "children": [ /* ... */ ] }
  ]
}
```

| Field         | Type     | Description                                         |
|---------------|----------|-----------------------------------------------------|
| `jdmId`       | string   | ZEN decision table ID                               |
| `input`       | []string | Context paths to project into decision input        |
| `trueKey`     | string   | Child node ID for truthy result                     |
| `falseKey`    | string   | Child node ID for falsy result                      |
| `branchField` | string   | Output field to read (falls back to `branch`/`result`) |

### switch — Multi-Branch Routing

Routes to one of multiple branches based on ZEN decision output.

```json
{
  "id": "order-router",
  "type": "switch",
  "spec": {
    "jdmId": "order-type-router",
    "input": ["data.order_type", "data.priority"],
    "cases": {
      "standard": "standard-flow",
      "express": "express-flow",
      "bulk": "bulk-flow"
    },
    "default": "fallback-flow"
  },
  "children": [
    { "id": "standard-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "express-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "bulk-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "fallback-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] }
  ]
}
```

| Field     | Type            | Description                                |
|-----------|-----------------|--------------------------------------------|
| `jdmId`   | string          | ZEN decision table ID                      |
| `input`   | []string        | Context paths for decision input           |
| `cases`   | map[string]string | Decision output → child node ID mapping  |
| `default` | string          | Child node ID for unmatched cases          |

### forEach — Iteration

Iterates over an array, executing children for each item.

```json
{
  "id": "process-items",
  "type": "forEach",
  "spec": {
    "over": "data.items",
    "as": "item",
    "indexAs": "idx",
    "maxItems": 100,
    "parallel": false
  },
  "children": [
    { "id": "process-item", "type": "action", "spec": { /* ... */ } }
  ]
}
```

| Field      | Type   | Default  | Description                          |
|------------|--------|----------|--------------------------------------|
| `over`     | string | required | Context path to array                |
| `as`       | string | `"item"` | Variable name for current item       |
| `indexAs`  | string | `"idx"`  | Variable name for current index      |
| `maxItems` | int    | 100      | Maximum items to process (safety)    |
| `parallel` | bool   | `false`  | Process items concurrently           |

---

## Leaf Nodes

### action — Connection Operation

Executes an operation against a configured connection.

```json
{
  "id": "fetch-order",
  "type": "action",
  "spec": {
    "connection": "pg-main",
    "operation": {
      "kind": "query",
      "payload": {
        "sql": "SELECT * FROM orders WHERE id = $1",
        "params": [{ "value": "{{input.order_id}}", "as": "int" }]
      }
    },
    "saveAs": "order",
    "resilience": {
      "timeoutMs": 5000,
      "retries": 3,
      "breakerOff": false
    },
    "onError": "fail",
    "idempotencyKeyFrom": "{{input.request_id}}",
    "unwrapSingleRow": true
  }
}
```

| Field                | Type    | Description                                      |
|----------------------|---------|--------------------------------------------------|
| `connection`         | string  | Connection registry key                          |
| `operation.kind`     | string  | Operation type (see below)                       |
| `operation.payload`  | object  | Kind-specific parameters                         |
| `saveAs`             | string  | Context path to store result                     |
| `resilience`         | object  | Per-action resilience overrides                  |
| `onError`            | string  | `"fail"` (default) or `"continue"`               |
| `idempotencyKeyFrom` | string  | Template for idempotency key                     |
| `unwrapSingleRow`    | bool    | Extract single row from result array             |

Operation kinds by connection type:

| Connection | Kind      | Payload Fields                              |
|------------|-----------|---------------------------------------------|
| postgres   | `query`   | `sql`, `params`                             |
| postgres   | `exec`    | `sql`, `params`                             |
| valkey     | `get`     | `key`                                       |
| valkey     | `set`     | `key`, `value`, `ttl`, `nx`                 |
| valkey     | `del`     | `key` or `keys`                             |
| rest/http  | `http`    | `method`, `path`, `body`, `headers`, `query`|
| kafka      | `publish` | `topic`, `key`, `value`, `headers`          |

### decision — ZEN Evaluation

Evaluates a ZEN decision table and stores the full result.

```json
{
  "id": "calculate-discount",
  "type": "decision",
  "spec": {
    "jdmId": "discount-calculator",
    "input": ["data.total", "data.customer_tier"],
    "saveAs": "discount"
  }
}
```

| Field    | Type     | Description                        |
|----------|----------|------------------------------------|
| `jdmId`  | string   | ZEN decision table ID              |
| `input`  | []string | Context paths for decision input   |
| `saveAs` | string   | Context path to store result       |

### set — Context Assignment

Sets a value in the execution context.

```json
{
  "id": "set-response-id",
  "type": "set",
  "spec": {
    "targetPath": "response.order_id",
    "from": "data.order.id",
    "omitEmpty": true
  }
}
```

Or with a literal value:

```json
{
  "id": "set-status",
  "type": "set",
  "spec": {
    "targetPath": "response.status",
    "value": "completed"
  }
}
```

| Field        | Type   | Description                               |
|--------------|--------|-------------------------------------------|
| `targetPath` | string | Destination path in context               |
| `value`      | any    | Literal value to set (mutually exclusive) |
| `from`       | string | Source path (mutually exclusive with value)|
| `omitEmpty`  | bool   | Skip if source is empty/null              |

### logger — Structured Logging

Emits a structured log entry with captured context values.

```json
{
  "id": "log-order",
  "type": "logger",
  "spec": {
    "label": "order-received",
    "level": "info",
    "capture": ["input.order_id", "data.status", "data.total"],
    "sampleRate": 0.1
  }
}
```

| Field        | Type     | Default  | Description                       |
|--------------|----------|----------|-----------------------------------|
| `label`      | string   | required | Log message identifier            |
| `level`      | string   | `"info"` | `info`, `warn`, `error`, `debug`  |
| `capture`    | []string | `[]`     | Context paths to include          |
| `sampleRate` | float64  | 1.0      | Sampling rate (0.0-1.0)           |

### response — HTTP Response

Sets the HTTP response for trigger flows.

```json
{
  "id": "respond-success",
  "type": "response",
  "spec": {
    "status": 200,
    "bodyFrom": "data.result"
  }
}
```

| Field      | Type   | Default | Description                     |
|------------|--------|---------|----------------------------------|
| `status`   | int    | 200     | HTTP status code                 |
| `bodyFrom` | string | -       | Context path for response body   |

---

## Collection Nodes

These nodes operate on arrays with per-item ZEN evaluation.

### filter — Array Filtering

Filters an array by evaluating a ZEN predicate per item.

```json
{
  "id": "filter-active",
  "type": "filter",
  "spec": {
    "over": "data.products",
    "jdmId": "product-active-filter",
    "input": ["price", "status", "category"],
    "saveAs": "activeProducts",
    "maxItems": 500
  }
}
```

### find — First Match

Finds the first item matching a ZEN predicate.

```json
{
  "id": "find-premium",
  "type": "find",
  "spec": {
    "over": "data.products",
    "jdmId": "premium-product-match",
    "input": ["sku", "tier"],
    "saveAs": "premiumProduct",
    "maxItems": 500
  }
}
```

### map — Array Transformation

Transforms each array item via ZEN evaluation.

```json
{
  "id": "transform-items",
  "type": "map",
  "spec": {
    "over": "data.items",
    "jdmId": "item-transformer",
    "input": ["name", "price", "quantity"],
    "saveAs": "transformedItems",
    "maxItems": 500
  }
}
```

### reduce — Aggregation

Aggregates an array to a single value via ZEN evaluation.

```json
{
  "id": "sum-totals",
  "type": "reduce",
  "spec": {
    "over": "data.items",
    "jdmId": "total-aggregator",
    "input": ["amount"],
    "saveAs": "grandTotal",
    "maxItems": 500,
    "initialValue": 0
  }
}
```

| Field          | Type   | Description                       |
|----------------|--------|-----------------------------------|
| `over`         | string | Context path to array             |
| `jdmId`        | string | ZEN decision table ID             |
| `input`        | []string | Item fields for decision input  |
| `saveAs`       | string | Context path to store result      |
| `maxItems`     | int    | Maximum items (default 500)       |
| `initialValue` | any    | Starting value (reduce only)      |

---

## Complete Flow Example

A complete order lookup flow with validation, database query, decision logic, and response:

```json
{
  "id": "order-flow",
  "tree": {
    "id": "trigger-1",
    "type": "trigger",
    "spec": {
      "method": "GET",
      "path": "/orders/{order_id}",
      "input": {
        "params": ["order_id"]
      }
    },
    "children": [
      {
        "id": "seq-main",
        "type": "sequence",
        "spec": { "stopOnError": true },
        "children": [
          {
            "id": "log-request",
            "type": "logger",
            "spec": {
              "label": "order-lookup",
              "level": "info",
              "capture": ["input.order_id"]
            }
          },
          {
            "id": "fetch-order",
            "type": "action",
            "spec": {
              "connection": "pg-main",
              "operation": {
                "kind": "query",
                "payload": {
                  "sql": "SELECT * FROM orders WHERE id = $1",
                  "params": [{ "value": "{{input.order_id}}", "as": "int" }]
                }
              },
              "saveAs": "order",
              "unwrapSingleRow": true,
              "resilience": {
                "timeoutMs": 3000,
                "retries": 2
              }
            }
          },
          {
            "id": "check-payment",
            "type": "condition",
            "spec": {
              "jdmId": "payment-status-check",
              "input": ["order.payment_status"],
              "trueKey": "paid-branch",
              "falseKey": "pending-branch",
              "branchField": "isPaid"
            },
            "children": [
              {
                "id": "paid-branch",
                "type": "set",
                "spec": {
                  "targetPath": "response.action",
                  "value": "proceed_fulfillment"
                }
              },
              {
                "id": "pending-branch",
                "type": "set",
                "spec": {
                  "targetPath": "response.action",
                  "value": "await_payment"
                }
              }
            ]
          },
          {
            "id": "set-order-data",
            "type": "set",
            "spec": {
              "targetPath": "response.order",
              "from": "order"
            }
          },
          {
            "id": "respond",
            "type": "response",
            "spec": {
              "status": 200,
              "bodyFrom": "response"
            }
          }
        ]
      }
    ]
  }
}
```

## Request/Response Example

```sh
curl -X GET http://localhost:8080/orders/12345
```

Response:

```json
{
  "order": {
    "id": 12345,
    "customer_id": 789,
    "total": 199.90,
    "payment_status": "PAID",
    "created_at": "2024-01-15T10:30:00Z"
  },
  "action": "proceed_fulfillment"
}
```

## Best Practices

1. **Unique node IDs** — Every node needs a unique `id` within the flow for tracing and debugging.

2. **Control depth limits** — Static depth is limited to 32 levels; keep flows flat where possible.

3. **Use sequences for ordering** — When steps must execute in order, wrap them in a `sequence` node.

4. **Set resilience per-action** — Override timeout/retry for actions that need different behavior than the connection default.

5. **Limit collection operations** — Use `maxItems` on filter/map/reduce to prevent runaway processing.

6. **Log at key points** — Add logger nodes at decision points with relevant context capture.
