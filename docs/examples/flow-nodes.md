# Flow Nodes Reference — complete node types and configuration

The rule engine flow interpreter executes a tree of typed nodes. Each node has an
`id`, `type`, `spec` (type-specific configuration), and optional `children`. This
guide covers all 17 node types with concrete config examples. Every type in the
taxonomy is executed by the interpreter — control nodes own children and run them;
leaf nodes do one unit of work and must not have children.

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

## Context Paths

Several specs reference values by a dotted **context path** (`input`, `saveAs`,
`from`, `over`, `bodyFrom`, `capture`, …). Paths resolve against three roots,
most-derived first: **Response → Data → Input**. The resolver descends each root
as a map — it does **not** add an `input.`/`data.`/`response.` namespace prefix
for you.

- **Request values** (path params, query, headers, parsed `body`) live at the top
  of the Input root. Reference them **directly by key**: `greet`, `order_id`,
  `body.amount` (the whole parsed body is under the `body` key). Do **not** write
  `input.greet`.
- **Node outputs** are stored under the node's `saveAs` key in the Data root.
  Reference them as `<saveAs>.<field>` — e.g. an action with `"saveAs": "order"`
  exposes `order.payment_status`; a decision with `"saveAs": "greeting"` exposes
  `greeting.message`. Do **not** write `data.order...`.
- **Response-building** targets (a `set` node's `targetPath`) write under the
  Response root; `targetPath: "response.message"` nests `message` under a key
  named `response`, and a terminal `response` node's `bodyFrom: "response"`
  returns that object.

A stray `input.`/`data.` prefix is the most common cause of a path silently not
resolving (an empty decision input, a condition that never matches, or a `set`
"source path not found" error).

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
| `load`            | leaf     | Load data from inline/file/URL             |

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

Executes children one after another, in declaration order, threading the same
context so a later step can read what an earlier step wrote. Stops on the first
error by default. A `response` child short-circuits the sequence — any siblings
after it do not run.

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

**Why sequence matters:** leaf nodes (`action`, `decision`, `set`, `response`,
`filter`, `find`, `map`, `reduce`, `logger`, `load`) **cannot have children** —
they do one thing and stop. To run several leaf steps in order you wrap them in a
`sequence` (a control node). This is also how you give a `condition` or `switch`
branch more than one step: point the branch at a `sequence` that holds the steps.

**`stopOnError` behavior:**

- `true` (default) — the first child error aborts the sequence; later siblings do
  not run and the error propagates. Use for steps that depend on each other.
- `false` — a failed child is skipped and the sequence keeps walking (best-effort).
  Use for independent, non-critical steps (e.g. a notification that shouldn't fail
  the whole flow). A mid-sequence `response` still stops the walk even in this mode.

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
    "falseKey": "await-payment"
  },
  "children": [
    { "id": "process-payment", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "await-payment", "type": "sequence", "spec": {}, "children": [ /* ... */ ] }
  ]
}
```

The decision returns a boolean `result`; a truthy result selects `trueKey`.

| Field         | Type     | Description                                         |
|---------------|----------|-----------------------------------------------------|
| `jdmId`       | string   | ZEN decision table ID                               |
| `input`       | []string | Context keys to project into the decision input (no `input.` prefix — e.g. `greet`, `body.amount`, `order.payment_status`) |
| `trueKey`     | string   | Child node ID taken when the result is truthy       |
| `falseKey`    | string   | Child node ID taken otherwise (use `""` to fall through and keep walking) |
| `branchField` | string   | Optional. When set, the condition reads this output field as a **string** and matches it against `trueKey`/`falseKey` (the field must hold the branch node ID, not a boolean). Omit it to use the truthy `result` convention. |

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
              "falseKey": "pending-branch"
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

## Decision-Driven Linear Flow

When the "if" logic can live in a decision table, keep the flow a straight line:
run one `decision`, copy its output into the response, and reply. This is the
easiest shape to read, and non-technical authors edit the branching as rows in
the decision table rather than as flow branches.

The decision table outputs the final value (e.g. a `message` field); the flow
copies it out and responds.

```json
{
  "id": "trigger",
  "type": "trigger",
  "spec": { "method": "GET", "path": "/mad/{greet}", "input": { "params": ["greet"] } },
  "children": [
    {
      "id": "main-seq",
      "type": "sequence",
      "spec": {},
      "children": [
        {
          "id": "decide-greeting",
          "type": "decision",
          "spec": { "jdmId": "greet-check", "input": ["greet"], "saveAs": "greeting" }
        },
        {
          "id": "set-message",
          "type": "set",
          "spec": { "targetPath": "response.message", "from": "greeting.message" }
        },
        {
          "id": "respond",
          "type": "response",
          "spec": { "status": 200, "bodyFrom": "response" }
        }
      ]
    }
  ]
}
```

Note the condition's `input` names context keys **without** an `input.` prefix.
Path params and body fields live directly in the context (`greet`, `body.amount`),
and data saved by an action/decision node is read by its `saveAs` key
(`greeting.message`). A stray `input.` prefix is the most common reason a decision
sees empty input and a branch never matches.

## Multiple Checks (Sequential Conditions)

To run several checks in order, chain `condition` nodes inside a `sequence`,
using the "set a default, override as each check passes" pattern. Each
`condition` uses `falseKey: ""` to fall through (keep whatever is set) when it
does not match, and its match branch is a `sequence` so it can do several steps
and then run the next check.

This example checks payment status, then (only if paid) the amount tier:

```json
{
  "id": "trigger",
  "type": "trigger",
  "spec": { "method": "POST", "path": "/orders/check", "input": { "body": true } },
  "children": [
    {
      "id": "main",
      "type": "sequence",
      "spec": {},
      "children": [
        {
          "id": "set-default",
          "type": "set",
          "spec": { "targetPath": "response.status", "value": "rejected: unpaid" }
        },
        {
          "id": "check-paid",
          "type": "condition",
          "spec": { "jdmId": "payment-paid-check", "input": ["body.payment_status"], "trueKey": "paid-seq", "falseKey": "" },
          "children": [
            {
              "id": "paid-seq",
              "type": "sequence",
              "spec": {},
              "children": [
                {
                  "id": "set-approved",
                  "type": "set",
                  "spec": { "targetPath": "response.status", "value": "approved: standard" }
                },
                {
                  "id": "check-high",
                  "type": "condition",
                  "spec": { "jdmId": "amount-high-check", "input": ["body.amount"], "trueKey": "high-set", "falseKey": "" },
                  "children": [
                    {
                      "id": "high-set",
                      "type": "set",
                      "spec": { "targetPath": "response.status", "value": "approved: high-value, needs review" }
                    }
                  ]
                }
              ]
            }
          ]
        },
        {
          "id": "respond",
          "type": "response",
          "spec": { "status": 200, "bodyFrom": "response" }
        }
      ]
    }
  ]
}
```

Behavior for `POST /orders/check`:

| `payment_status` | `amount` | `response.status`                      |
|------------------|----------|----------------------------------------|
| `PAID`           | `>= 1000`| `approved: high-value, needs review`   |
| `PAID`           | `< 1000` | `approved: standard`                   |
| anything else    | —        | `rejected: unpaid`                     |

The two decision tables (`payment-paid-check`, `amount-high-check`) each return a
truthy `result` field; see [decision-tables.md](./decision-tables.md) for how a
`condition` reads the decision output.

## Best Practices

1. **Unique node IDs** — Every node needs a unique `id` within the flow for tracing and debugging.

2. **Control depth limits** — Static depth is limited to 32 levels; keep flows flat where possible.

3. **Use sequences for ordering** — Leaf nodes cannot have children, so to run several leaf steps in order (or to give a condition/switch branch more than one step) wrap them in a `sequence`. Control nodes (`trigger`, `sequence`, `condition`, `switch`, `parallel`, `forEach`) own children; leaf nodes (`action`, `decision`, `set`, `response`, `filter`, `find`, `map`, `reduce`, `logger`, `load`) do not.

4. **Set resilience per-action** — Override timeout/retry for actions that need different behavior than the connection default.

5. **Limit collection operations** — Use `maxItems` on filter/map/reduce to prevent runaway processing.

6. **Log at key points** — Add logger nodes at decision points with relevant context capture.
