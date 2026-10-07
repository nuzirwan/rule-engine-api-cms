# Flow Fixtures — Test your flows with reproducible scenarios

Fixtures are test scenarios bundled with a flow. They define input, mock external
responses, and expected output. The engine replays fixtures during validation to
ensure flows behave correctly before publishing.

**Related docs:**
- [dry-run-validation.md](./dry-run-validation.md) — Validation and dry-run endpoints
- [slice-f-admin-api.md](../architecture/lld/slice-f-admin-api.md) — Admin API contract

---

## How Fixtures Work

```
┌─────────────────────────────────────────────────────────────────────┐
│                        FIXTURE REPLAY FLOW                          │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  1. Author defines fixtures in CMS                                  │
│         ↓                                                           │
│  2. CMS sends fixtures with flow to engine via POST /admin/flows    │
│         ↓                                                           │
│  3. On validate: engine replays each fixture                        │
│         • Input → builds flow context                               │
│         • Mocks → replaces real connector responses                 │
│         • Run flow with mock registry                               │
│         • Compare output to expected (Want)                         │
│         ↓                                                           │
│  4. All fixtures pass → ok:true, version can be published           │
│     Any fixture fails → ok:false, publish blocked                   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Fixture Structure

### Persisted Fixture (FlowFixture)

Stored with the flow version in the database:

```json
{
  "name": "happy path - paid order",
  "input": {
    "order_id": "ORD-123",
    "msisdn": "628111015450"
  },
  "want": {
    "status": 200,
    "body": {
      "order_id": "ORD-123",
      "action": "proceed_fulfillment"
    }
  }
}
```

| Field   | Type              | Description                                    |
|---------|-------------------|------------------------------------------------|
| `name`  | string            | Human-readable fixture name                    |
| `input` | map[string]any    | Input values passed to flow context            |
| `want`  | map[string]any    | Expected response output (deep-equal checked)  |

### Admin Fixture (for validation)

Extended fixture format used during validation with mocks:

```json
{
  "name": "happy path - paid order",
  "input": {
    "order_id": "ORD-123"
  },
  "mocks": {
    "pg-main:query": {
      "rows": [
        { "order_id": "ORD-123", "payment_status": "PAID", "amount": 50000 }
      ]
    },
    "valkey-cache:get": {
      "result": null
    }
  },
  "expect": {
    "output": {
      "status": 200,
      "body": {
        "order_id": "ORD-123",
        "action": "proceed_fulfillment"
      }
    },
    "errors": []
  }
}
```

| Field    | Type                        | Description                                |
|----------|-----------------------------|--------------------------------------------|
| `name`   | string                      | Fixture identifier                         |
| `input`  | map[string]any              | Flow input (path params, body, etc.)       |
| `mocks`  | map[string]map[string]any   | Mock responses keyed by `connection:op`    |
| `expect` | object                      | Expected outcomes                          |
| `expect.output`     | map[string]any | Expected response                          |
| `expect.branchPath` | []string       | Expected branch sequence (optional)        |
| `expect.errors`     | []string       | Expected error patterns (optional)         |

---

## Mock Registry Format

Mocks replace real connector responses during fixture replay:

```json
{
  "mocks": {
    "<connection>:<operation>": { /* mock response */ }
  }
}
```

### Database Mock

```json
{
  "pg-main:query": {
    "rows": [
      { "id": 1, "name": "Order #1", "status": "PAID" },
      { "id": 2, "name": "Order #2", "status": "PENDING" }
    ]
  }
}
```

### Cache Mock (Valkey)

```json
{
  "valkey-cache:get": {
    "result": { "cached_value": "data" }
  },
  "valkey-cache:get:miss": {
    "result": null
  }
}
```

### REST API Mock

```json
{
  "shipping-api:http": {
    "status": 200,
    "body": {
      "tracking_number": "TRK-456",
      "estimated_delivery": "2024-01-15"
    }
  }
}
```

---

## Example: Order Status Flow with Fixtures

### Flow Definition

```json
{
  "id": "trigger",
  "type": "trigger",
  "spec": {
    "method": "GET",
    "path": "/order/{order_id}",
    "input": { "params": ["order_id"] }
  },
  "children": [
    {
      "id": "fetch-order",
      "type": "action",
      "spec": {
        "connection": "fmc-pg",
        "operation": {
          "kind": "query",
          "payload": {
            "sql": "SELECT * FROM order_status WHERE order_id = $1",
            "params": [{ "value": "{{input.order_id}}", "as": "string" }]
          }
        },
        "saveAs": "order"
      },
      "children": [
        {
          "id": "check-payment",
          "type": "condition",
          "spec": {
            "jdmId": "payment-status-check",
            "trueKey": "paid",
            "falseKey": "unpaid"
          },
          "children": [
            {
              "id": "paid",
              "type": "set",
              "spec": { "targetPath": "action", "value": "proceed_fulfillment" },
              "children": [{ "id": "respond-paid", "type": "response", "spec": { "status": 200 } }]
            },
            {
              "id": "unpaid",
              "type": "set",
              "spec": { "targetPath": "action", "value": "await_payment" },
              "children": [{ "id": "respond-unpaid", "type": "response", "spec": { "status": 200 } }]
            }
          ]
        }
      ]
    }
  ]
}
```

### Fixtures for This Flow

```json
{
  "fixtures": [
    {
      "name": "paid order returns proceed_fulfillment",
      "input": { "order_id": "ORD-PAID-001" },
      "mocks": {
        "fmc-pg:query": {
          "rows": [{ "order_id": "ORD-PAID-001", "payment_status": "PAID", "amount": 100000 }]
        }
      },
      "expect": {
        "output": {
          "order_id": "ORD-PAID-001",
          "action": "proceed_fulfillment"
        }
      }
    },
    {
      "name": "unpaid order returns await_payment",
      "input": { "order_id": "ORD-UNPAID-002" },
      "mocks": {
        "fmc-pg:query": {
          "rows": [{ "order_id": "ORD-UNPAID-002", "payment_status": "PENDING", "amount": 50000 }]
        }
      },
      "expect": {
        "output": {
          "order_id": "ORD-UNPAID-002",
          "action": "await_payment"
        }
      }
    },
    {
      "name": "order not found returns empty",
      "input": { "order_id": "ORD-NOTFOUND" },
      "mocks": {
        "fmc-pg:query": { "rows": [] }
      },
      "expect": {
        "errors": ["order not found"]
      }
    }
  ]
}
```

---

## CMS Integration

### Creating Fixtures in CMS

In the Strapi CMS, fixtures are authored via the Flow content type's fixtures field:

1. **Navigate to Flow Editor**
   - Content Manager → Flows → Select or create a flow

2. **Open Fixtures Panel**
   - Below the flow canvas, find the "Fixtures" section
   - Click "Add Fixture" to create a new test case

3. **Define Fixture**
   ```
   Name:   paid order - happy path
   
   Input (JSON):
   {
     "order_id": "ORD-123",
     "customer_id": "CUST-456"
   }
   
   Expected Output (JSON):
   {
     "status": 200,
     "body": { "action": "proceed_fulfillment" }
   }
   ```

4. **Save Flow**
   - Fixtures are saved with the flow version

### Triggering Validation from CMS

When you save or attempt to publish a flow, the CMS:

1. **Calls `POST /admin/flows`** — Creates a new flow version with fixtures
2. **Calls `POST /admin/flows/validate`** — Validates structure + replays fixtures
3. **Displays validation result** in ValidationPanel component

```typescript
// CMS admin-client.ts
async validateFlow(flowId: string, version: number): Promise<ValidateFlowResponse> {
  const body: ValidateFlowRequest = { env: this.env, flowId, version };
  return this.request<ValidateFlowResponse>('POST', '/admin/flows/validate', body);
}
```

### Validation Panel UI

The CMS shows fixture results in the ValidationPanel:

```
┌─────────────────────────────────────────────────────────────────┐
│  Validation Results                                             │
├─────────────────────────────────────────────────────────────────┤
│  ✅ Structural validation passed                                │
│                                                                 │
│  Fixtures:                                                      │
│  ├── ✅ paid order - happy path                                 │
│  ├── ✅ unpaid order - await payment                            │
│  └── ❌ order not found                                         │
│         Expected: { "status": 404 }                             │
│         Actual:   { "status": 500, "error": "..." }             │
│                                                                 │
│  ⚠️  Validation failed. Fix issues before publishing.          │
└─────────────────────────────────────────────────────────────────┘
```

---

## Dry-Run from CMS

The CMS can also trigger dry-runs for interactive debugging:

### Using Dry-Run Panel

1. **Open Flow in CMS**
2. **Click "Dry Run" button**
3. **Enter test input:**
   ```json
   {
     "method": "GET",
     "path": "/order/ORD-123",
     "params": { "order_id": "ORD-123" }
   }
   ```
4. **View execution trace:**
   - Each node shows: ID, type, duration, branch taken
   - Write operations show `wrote: "suppressed"`

### CMS Dry-Run API Call

```typescript
// CMS admin-client.ts
async dryRunFlow(req: Omit<DryRunFlowRequest, 'env'>): Promise<unknown> {
  const body: DryRunFlowRequest = { env: this.env, ...req };
  return this.request<unknown>('POST', '/admin/flows/dry-run', body);
}
```

### Dry-Run Request

```sh
curl -X POST http://localhost:8080/admin/flows/dry-run \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "env": "",
    "flowId": "order-status-flow",
    "version": 3,
    "input": {
      "method": "GET",
      "path": "/order/ORD-123",
      "params": { "order_id": "ORD-123" }
    },
    "mocks": {
      "fmc-pg:query": {
        "rows": [{ "order_id": "ORD-123", "payment_status": "PAID" }]
      }
    }
  }'
```

### Dry-Run Response (Trace)

```json
{
  "trace": [
    {
      "node_id": "trigger",
      "node_type": "trigger",
      "duration_ms": 0,
      "attrs": {}
    },
    {
      "node_id": "fetch-order",
      "node_type": "action",
      "duration_ms": 2,
      "attrs": { "connection": "fmc-pg", "operation": "query" }
    },
    {
      "node_id": "check-payment",
      "node_type": "condition",
      "branch_taken": "paid",
      "duration_ms": 1,
      "attrs": { "jdmId": "payment-status-check", "result": true }
    },
    {
      "node_id": "paid",
      "node_type": "set",
      "duration_ms": 0,
      "attrs": { "targetPath": "action", "value": "proceed_fulfillment" }
    },
    {
      "node_id": "respond-paid",
      "node_type": "response",
      "duration_ms": 0,
      "attrs": { "status": 200 }
    }
  ],
  "response": {
    "order_id": "ORD-123",
    "payment_status": "PAID",
    "action": "proceed_fulfillment"
  },
  "errors": []
}
```

---

## Publish Flow with Fixtures

### Step-by-Step Workflow

1. **Create/Update Flow Version**
   ```sh
   curl -X POST http://localhost:8080/admin/flows \
     -H "Authorization: Bearer $ADMIN_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{
       "env": "",
       "flowId": "order-status",
       "method": "GET",
       "path": "/order/{order_id}",
       "tree": { /* flow tree */ },
       "fixtures": [
         { "name": "test 1", "input": {...}, "want": {...} },
         { "name": "test 2", "input": {...}, "want": {...} }
       ]
     }'
   ```
   
   Response: `{ "flowId": "order-status", "version": 1, "validated": false }`

2. **Validate (Replays Fixtures)**
   ```sh
   curl -X POST http://localhost:8080/admin/flows/validate \
     -H "Authorization: Bearer $ADMIN_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{ "env": "", "flowId": "order-status", "version": 1 }'
   ```
   
   Response:
   ```json
   {
     "ok": true,
     "structural": [],
     "fixtures": [
       { "name": "test 1", "passed": true },
       { "name": "test 2", "passed": true }
     ]
   }
   ```
   
   On success, version is marked `validated: true`.

3. **Publish (Requires Validated)**
   ```sh
   curl -X POST http://localhost:8080/admin/flows/order-status/publish \
     -H "Authorization: Bearer $ADMIN_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{ "env": "", "version": 1 }'
   ```
   
   Response: `{ "flowId": "order-status", "activeVersion": 1 }`

   **Note:** Publishing an unvalidated version returns `422 Unprocessable Entity`.

---

## Best Practices

### 1. Cover All Branches

Each condition/switch should have fixtures for every branch:

```json
{
  "fixtures": [
    { "name": "condition true branch", "input": {...}, "want": {...} },
    { "name": "condition false branch", "input": {...}, "want": {...} },
    { "name": "switch case A", "input": {...}, "want": {...} },
    { "name": "switch case B", "input": {...}, "want": {...} },
    { "name": "switch default", "input": {...}, "want": {...} }
  ]
}
```

### 2. Test Edge Cases

```json
{
  "fixtures": [
    { "name": "empty result set", "mocks": { "pg:query": { "rows": [] } } },
    { "name": "null values", "input": { "order_id": null } },
    { "name": "very long input", "input": { "name": "A".repeat(1000) } }
  ]
}
```

### 3. Use Descriptive Names

```json
{
  "fixtures": [
    { "name": "PAID status with amount > 0 returns proceed_fulfillment" },
    { "name": "PENDING status returns await_payment" },
    { "name": "REFUNDED status returns refund_complete" }
  ]
}
```

### 4. Mock External Dependencies

Always mock external calls to ensure reproducibility:

```json
{
  "mocks": {
    "payment-gateway:http": { "status": 200, "body": { "approved": true } },
    "inventory-service:http": { "status": 200, "body": { "available": 50 } }
  }
}
```

### 5. Version Your Fixtures

When flow logic changes, update fixtures to match:

- Add new fixtures for new branches
- Update expected output when response format changes
- Remove obsolete fixtures when features are removed

---

## Troubleshooting

### Fixture Fails with "output mismatch"

```json
{
  "name": "test case",
  "passed": false,
  "diff": {
    "field": "output",
    "expected": { "action": "proceed" },
    "actual": { "action": "proceed_fulfillment" }
  }
}
```

**Fix:** Update `want` to match actual output, or fix the flow logic.

### Fixture Fails with "error"

```json
{
  "name": "test case",
  "passed": false,
  "diff": {
    "field": "error",
    "expected": null,
    "actual": "connection 'pg-main' not found"
  }
}
```

**Fix:** Ensure mocks cover all connections used by the flow.

### Structural Validation Fails

Fixtures are only replayed if structural validation passes. Fix structural issues first:

```json
{
  "ok": false,
  "structural": [
    { "code": "dangling_connection", "message": "connection 'pg-main' not found" }
  ],
  "fixtures": []  // Not run because structural failed
}
```

**Fix:** Create the missing connection or update the flow to use an existing one.
