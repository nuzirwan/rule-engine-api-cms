# Decision Tables Reference — ZEN JDM integration

The rule engine uses ZEN JDM (JSON Decision Model) for business logic evaluation.
Decision tables provide a declarative way to encode conditions and computations
without writing code. This guide covers how nodes interact with ZEN decisions.

**Related architecture:** [slice-c-zen-auth.md](../architecture/lld/slice-c-zen-auth.md)

## Overview

ZEN decisions are evaluated by multiple node types:

| Node Type   | Purpose                                      |
|-------------|----------------------------------------------|
| `condition` | Binary branching (true/false)                |
| `switch`    | Multi-branch routing                         |
| `decision`  | Compute and store values                     |
| `filter`    | Per-item predicate for array filtering       |
| `find`      | Per-item predicate to find first match       |
| `map`       | Per-item transformation                      |
| `reduce`    | Per-item aggregation                         |

## Evaluator Interface

All ZEN evaluations use this interface:

```go
type Evaluator interface {
    Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error)
}
```

- `jdmID`: The decision table identifier stored in the config store
- `input`: Key-value map projected from the execution context
- Returns: Output map from the decision evaluation

---

## Input Projection

The `input` array in node specs names context paths to project into the decision:

```json
{
  "jdmId": "payment-check",
  "input": ["data.payment_status", "data.amount", "input.customer_tier"]
}
```

This becomes a flat map keyed by the last path segment:

```json
{
  "payment_status": "PAID",
  "amount": 199.90,
  "customer_tier": "gold"
}
```

The decision table receives this map and can reference `payment_status`, `amount`,
and `customer_tier` in its rules.

---

## Binary Conditions

The `condition` node evaluates a decision and branches based on a truthy output field.

### Node Configuration

```json
{
  "id": "payment-check",
  "type": "condition",
  "spec": {
    "jdmId": "payment-status-check",
    "input": ["data.payment_status", "data.amount"],
    "trueKey": "process-paid",
    "falseKey": "handle-pending",
    "branchField": "approved"
  },
  "children": [
    { "id": "process-paid", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "handle-pending", "type": "sequence", "spec": {}, "children": [ /* ... */ ] }
  ]
}
```

### Field Resolution

The condition node determines which branch to take:

1. If `branchField` is set, reads that field from the decision output
2. Otherwise falls back to `out["branch"]` then `out["result"]`
3. Converts the value to boolean using truthy semantics

### Example Decision Table

A decision table that returns `{"approved": true}` or `{"approved": false}`:

| payment_status | amount   | approved |
|----------------|----------|----------|
| "PAID"         | -        | true     |
| "PENDING"      | >= 100   | false    |
| "PENDING"      | < 100    | false    |
| -              | -        | false    |

### Request/Response Example

```sh
curl -X POST http://localhost:8080/orders/check \
  -H "Content-Type: application/json" \
  -d '{"payment_status": "PAID", "amount": 50}'
```

Decision evaluates with input:
```json
{ "payment_status": "PAID", "amount": 50 }
```

Decision returns:
```json
{ "approved": true }
```

Flow takes `process-paid` branch.

---

## Multi-Branch Routing

The `switch` node routes to different branches based on a decision output value.

### Node Configuration

```json
{
  "id": "order-router",
  "type": "switch",
  "spec": {
    "jdmId": "order-type-classifier",
    "input": ["data.order_type", "data.priority", "data.region"],
    "cases": {
      "standard": "standard-flow",
      "express": "express-flow",
      "bulk": "bulk-flow",
      "international": "intl-flow"
    },
    "default": "fallback-flow"
  },
  "children": [
    { "id": "standard-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "express-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "bulk-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "intl-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] },
    { "id": "fallback-flow", "type": "sequence", "spec": {}, "children": [ /* ... */ ] }
  ]
}
```

### Example Decision Table

A decision table that classifies orders:

| order_type   | priority | region         | route          |
|--------------|----------|----------------|----------------|
| "retail"     | "high"   | -              | "express"      |
| "retail"     | -        | -              | "standard"     |
| "wholesale"  | -        | -              | "bulk"         |
| -            | -        | "international"| "international"|
| -            | -        | -              | "standard"     |

The switch node reads the `route` output (or the first output key) and maps it
to the corresponding child node ID via the `cases` map.

---

## Value Computation

The `decision` node evaluates a decision table and stores the full output map.

### Node Configuration

```json
{
  "id": "calculate-discount",
  "type": "decision",
  "spec": {
    "jdmId": "discount-calculator",
    "input": ["data.total", "data.customer_tier", "data.item_count"],
    "saveAs": "discount"
  }
}
```

### Example Decision Table

A discount calculator:

| customer_tier | total  | item_count | discount_pct | discount_type |
|---------------|--------|------------|--------------|---------------|
| "platinum"    | -      | -          | 20           | "tier"        |
| "gold"        | >= 500 | -          | 15           | "tier+volume" |
| "gold"        | -      | -          | 10           | "tier"        |
| "silver"      | >= 500 | >= 10      | 10           | "volume"      |
| "silver"      | -      | -          | 5            | "tier"        |
| -             | >= 1000| -          | 5            | "volume"      |
| -             | -      | -          | 0            | "none"        |

### Using the Result

After the decision node executes, the result is available in the context:

```json
{
  "discount": {
    "discount_pct": 15,
    "discount_type": "tier+volume"
  }
}
```

Use a `set` node to apply it:

```json
{
  "id": "apply-discount",
  "type": "set",
  "spec": {
    "targetPath": "data.final_total",
    "from": "{{data.total * (1 - discount.discount_pct / 100)}}"
  }
}
```

---

## Collection Operations

Filter, find, map, and reduce nodes evaluate decisions per-item in an array.

### Filter — Array Filtering

```json
{
  "id": "filter-eligible",
  "type": "filter",
  "spec": {
    "over": "data.products",
    "jdmId": "product-eligibility",
    "input": ["price", "category", "in_stock"],
    "saveAs": "eligibleProducts",
    "maxItems": 500
  }
}
```

The decision is evaluated for each item. Items where the decision returns a truthy
result (via `result`, `match`, or first boolean field) are kept.

### Find — First Match

```json
{
  "id": "find-premium",
  "type": "find",
  "spec": {
    "over": "data.products",
    "jdmId": "is-premium-product",
    "input": ["sku", "tier", "price"],
    "saveAs": "premiumProduct",
    "maxItems": 500
  }
}
```

Returns the first item where the decision evaluates to truthy.

### Map — Transformation

```json
{
  "id": "enrich-products",
  "type": "map",
  "spec": {
    "over": "data.products",
    "jdmId": "product-enricher",
    "input": ["name", "price", "category"],
    "saveAs": "enrichedProducts",
    "maxItems": 500
  }
}
```

Each item is replaced with the decision output:

| name      | price | category     | display_name        | tax_rate |
|-----------|-------|--------------|---------------------|----------|
| "Widget"  | 10.00 | "electronics"| "Widget (Electronic)"| 0.08    |
| "Gadget"  | 25.00 | "home"       | "Gadget (Home)"     | 0.06     |

### Reduce — Aggregation

```json
{
  "id": "sum-totals",
  "type": "reduce",
  "spec": {
    "over": "data.items",
    "jdmId": "line-item-aggregator",
    "input": ["quantity", "unit_price"],
    "saveAs": "orderTotal",
    "maxItems": 500,
    "initialValue": 0
  }
}
```

The decision receives the current accumulator as `_acc` and returns the new value.

---

## Error Handling

Decision evaluation can fail in several ways:

| Error Class  | HTTP Status | Cause                                |
|--------------|-------------|--------------------------------------|
| `NotFound`   | 404         | JDM ID not in the config store       |
| `Validation` | 400         | Malformed JDM or invalid input       |
| `Timeout`    | 408         | Context deadline exceeded            |

### Handling Missing Decisions

If a referenced `jdmId` doesn't exist, the flow fails with a clear error:

```json
{
  "error": "decision not found: discount-calculator",
  "code": "JDM_NOT_FOUND"
}
```

Ensure all referenced decision tables are published before activating flows.

---

## Best Practices

1. **Keep decisions focused** — Each decision table should do one thing well. Use multiple decisions for complex logic.

2. **Use meaningful field names** — Decision input and output fields should be self-documenting.

3. **Test decisions separately** — Validate decision tables with the dry-run endpoint before using them in flows.

4. **Set maxItems on collections** — Prevent runaway processing by limiting collection operation sizes.

5. **Version decision tables** — Use the config store's versioning to track decision table changes.

6. **Document decision logic** — Decision tables encode business rules; document the expected inputs and outputs.

---

## Complete Example

A flow that uses multiple decision types:

```json
{
  "id": "order-processing",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": {
      "method": "POST",
      "path": "/orders/process",
      "input": { "body": true }
    },
    "children": [
      {
        "id": "seq-main",
        "type": "sequence",
        "spec": { "stopOnError": true },
        "children": [
          {
            "id": "filter-available",
            "type": "filter",
            "spec": {
              "over": "input.items",
              "jdmId": "item-availability-check",
              "input": ["sku", "quantity"],
              "saveAs": "availableItems",
              "maxItems": 100
            }
          },
          {
            "id": "calc-discount",
            "type": "decision",
            "spec": {
              "jdmId": "order-discount",
              "input": ["input.customer_tier", "availableItems"],
              "saveAs": "discount"
            }
          },
          {
            "id": "check-approval",
            "type": "condition",
            "spec": {
              "jdmId": "order-approval",
              "input": ["discount.final_total", "input.customer_id"],
              "trueKey": "auto-approve",
              "falseKey": "manual-review",
              "branchField": "auto_approved"
            },
            "children": [
              { "id": "auto-approve", "type": "set", "spec": { "targetPath": "response.status", "value": "approved" } },
              { "id": "manual-review", "type": "set", "spec": { "targetPath": "response.status", "value": "pending_review" } }
            ]
          }
        ]
      }
    ]
  }
}
```
