# Dry-Run and Validation Reference — testing flows without side effects

The rule engine provides validation and dry-run endpoints to test flows before
deploying to production. Validation checks structural correctness; dry-run
executes flows with write suppression. This guide covers both testing approaches.

**Related architecture:** [slice-f-admin-api.md](../architecture/lld/slice-f-admin-api.md)

## Overview

Two testing modes:

| Mode       | Purpose                                    | Side Effects |
|------------|--------------------------------------------|--------------|
| Validation | Check structural correctness               | None         |
| Dry-Run    | Execute flow with simulated input          | Writes suppressed |

---

## Validation Endpoint

```
POST /admin/flows/validate
```

Checks flow structure without executing it.

### Stored Flow Validation

Validate a flow already in the config store:

```sh
curl -X POST http://localhost:8080/admin/flows/validate \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flowId": "order-flow",
    "version": 1
  }'
```

### Inline Flow Validation

Validate a flow definition before storing:

```sh
curl -X POST http://localhost:8080/admin/flows/validate \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flow": {
      "tree": {
        "id": "trigger",
        "type": "trigger",
        "spec": {
          "method": "GET",
          "path": "/orders/{id}",
          "input": { "params": ["id"] }
        },
        "children": [
          {
            "id": "fetch",
            "type": "action",
            "spec": {
              "connection": "pg-main",
              "operation": { "kind": "query", "payload": { "sql": "SELECT 1" } },
              "saveAs": "result"
            }
          }
        ]
      }
    }
  }'
```

### Validation Response

Success:

```json
{
  "ok": true,
  "structural": [],
  "fixtures": []
}
```

Validation errors:

```json
{
  "ok": false,
  "structural": [
    {
      "code": "root_not_trigger",
      "message": "root node must be a trigger",
      "nodeId": "seq-1"
    },
    {
      "code": "missing_ref",
      "message": "connection 'pg-main' not found",
      "nodeId": "fetch"
    }
  ],
  "fixtures": []
}
```

---

## Validation Rules

| Code                  | Description                                   |
|-----------------------|-----------------------------------------------|
| `root_not_trigger`    | Root node must be `trigger` or `messageTrigger` |
| `nested_trigger`      | Trigger nodes only allowed at root            |
| `duplicate_id`        | Node IDs must be unique within the flow       |
| `unknown_type`        | Node type not recognized                      |
| `bad_spec`            | Spec doesn't parse or validate for type       |
| `control_no_children` | Control nodes require children                |
| `leaf_has_children`   | Leaf nodes cannot have children               |
| `max_depth`           | Static depth exceeds 32 levels                |
| `missing_ref`         | Referenced connection or JDM doesn't exist    |

### Example Violations

**Root not trigger:**

```json
{
  "tree": {
    "id": "seq-1",
    "type": "sequence",  // ❌ Should be trigger
    "spec": {},
    "children": []
  }
}
```

**Nested trigger:**

```json
{
  "tree": {
    "id": "trigger-1",
    "type": "trigger",
    "spec": { /* ... */ },
    "children": [
      {
        "id": "trigger-2",
        "type": "trigger",  // ❌ Triggers only at root
        "spec": { /* ... */ }
      }
    ]
  }
}
```

**Duplicate IDs:**

```json
{
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": { /* ... */ },
    "children": [
      { "id": "action-1", "type": "action", "spec": { /* ... */ } },
      { "id": "action-1", "type": "action", "spec": { /* ... */ } }  // ❌ Duplicate
    ]
  }
}
```

**Leaf with children:**

```json
{
  "id": "set-value",
  "type": "set",  // Leaf type
  "spec": { "targetPath": "foo", "value": "bar" },
  "children": [  // ❌ Leaf nodes cannot have children
    { "id": "other", "type": "logger", "spec": {} }
  ]
}
```

---

## Fixture Testing

Validate flows with test fixtures:

```sh
curl -X POST http://localhost:8080/admin/flows/validate \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flow": {
      "tree": { /* flow definition */ },
      "fixtures": [
        {
          "name": "happy path",
          "input": {
            "method": "GET",
            "path": "/orders/123",
            "params": { "id": "123" }
          },
          "expect": {
            "status": 200,
            "bodyContains": ["order_id", "123"]
          }
        },
        {
          "name": "not found",
          "input": {
            "method": "GET",
            "path": "/orders/999",
            "params": { "id": "999" }
          },
          "expect": {
            "status": 404
          }
        }
      ]
    }
  }'
```

Response with fixture results:

```json
{
  "ok": true,
  "structural": [],
  "fixtures": [
    { "name": "happy path", "passed": true, "error": null },
    { "name": "not found", "passed": true, "error": null }
  ]
}
```

Fixture failure:

```json
{
  "ok": false,
  "structural": [],
  "fixtures": [
    { "name": "happy path", "passed": false, "error": "expected status 200, got 500" }
  ]
}
```

---

## Dry-Run Endpoint

```
POST /admin/flows/dry-run
```

Executes a flow with write suppression and returns execution trace.

### Request

```sh
curl -X POST http://localhost:8080/admin/flows/dry-run \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flowId": "order-flow",
    "version": 1,
    "input": {
      "method": "GET",
      "path": "/orders/123",
      "params": { "id": "123" },
      "body": {}
    }
  }'
```

### Response (TraceRecord)

```json
{
  "trace_id": "abc123",
  "flow_id": "order-flow",
  "flow_version": 1,
  "environment": "development",
  "dry_run": true,
  "steps": [
    {
      "node_id": "trigger-1",
      "node_type": "trigger",
      "branch_taken": "",
      "duration_ms": 1,
      "attrs": {}
    },
    {
      "node_id": "fetch-order",
      "node_type": "action",
      "branch_taken": "",
      "duration_ms": 5,
      "attrs": {
        "connection": "pg-main",
        "operation": "query"
      }
    },
    {
      "node_id": "check-payment",
      "node_type": "condition",
      "branch_taken": "paid-branch",
      "duration_ms": 2,
      "attrs": {
        "jdmId": "payment-check",
        "result": true
      }
    },
    {
      "node_id": "update-status",
      "node_type": "action",
      "branch_taken": "",
      "duration_ms": 0,
      "attrs": {
        "wrote": "suppressed"
      }
    },
    {
      "node_id": "respond",
      "node_type": "response",
      "branch_taken": "",
      "duration_ms": 0,
      "attrs": {
        "status": 200
      }
    }
  ]
}
```

---

## Write Suppression

In dry-run mode, write operations are NOT executed:

| Operation    | Read/Write | Dry-Run Behavior                |
|--------------|------------|----------------------------------|
| `query`      | Read       | Executed normally                |
| `exec`       | Write      | Suppressed, returns mock result  |
| `get`        | Read       | Executed normally                |
| `set`        | Write      | Suppressed                       |
| `del`        | Write      | Suppressed                       |
| `http` GET   | Read       | Executed normally                |
| `http` POST  | Write      | Suppressed                       |
| `publish`    | Write      | Suppressed                       |

Suppressed operations record `"wrote": "suppressed"` in the trace attributes.

### Suppression Example

Flow with a write action:

```json
{
  "id": "create-order",
  "type": "action",
  "spec": {
    "connection": "pg-main",
    "operation": {
      "kind": "exec",
      "payload": {
        "sql": "INSERT INTO orders (customer_id) VALUES ($1) RETURNING id",
        "params": [{ "value": "{{input.customer_id}}", "as": "int" }]
      }
    },
    "saveAs": "newOrder"
  }
}
```

Dry-run trace shows:

```json
{
  "node_id": "create-order",
  "node_type": "action",
  "duration_ms": 0,
  "attrs": {
    "wrote": "suppressed",
    "connection": "pg-main",
    "operation": "exec"
  }
}
```

The `newOrder` saveAs receives a mock result, not actual database output.

---

## Complete Testing Workflow

### Step 1: Validate Structure

```sh
curl -X POST http://localhost:8080/admin/flows/validate \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flow": { "tree": { /* new flow */ } }
  }'
```

Check `ok: true` and no structural errors.

### Step 2: Publish to Development

```sh
curl -X POST http://localhost:8080/admin/flows \
  -H "Content-Type: application/json" \
  -d '{
    "id": "my-flow",
    "env": "development",
    "tree": { /* validated flow */ }
  }'
```

### Step 3: Dry-Run Test

```sh
curl -X POST http://localhost:8080/admin/flows/dry-run \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flowId": "my-flow",
    "input": {
      "method": "POST",
      "path": "/orders",
      "body": { "customer_id": 123, "items": [...] }
    }
  }'
```

Review the trace to verify:
- Correct branch taken at condition nodes
- Expected actions executed
- Write operations show "suppressed"

### Step 4: Promote to Production

After dry-run verification:

```sh
curl -X PUT http://localhost:8080/admin/flows/my-flow \
  -H "Content-Type: application/json" \
  -d '{
    "env": "production",
    "enabled": true
  }'
```

---

## Request/Response Examples

### Validation Success

```sh
curl -X POST http://localhost:8080/admin/flows/validate \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flow": {
      "tree": {
        "id": "trigger",
        "type": "trigger",
        "spec": { "method": "GET", "path": "/test" },
        "children": [
          { "id": "respond", "type": "response", "spec": { "status": 200 } }
        ]
      }
    }
  }'
```

Response:

```json
{ "ok": true, "structural": [], "fixtures": [] }
```

### Validation Failure

```sh
curl -X POST http://localhost:8080/admin/flows/validate \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flow": {
      "tree": {
        "id": "seq",
        "type": "sequence",
        "spec": {},
        "children": []
      }
    }
  }'
```

Response:

```json
{
  "ok": false,
  "structural": [
    { "code": "root_not_trigger", "message": "root node must be a trigger", "nodeId": "seq" },
    { "code": "control_no_children", "message": "control node requires children", "nodeId": "seq" }
  ],
  "fixtures": []
}
```

### Dry-Run with Branch

```sh
curl -X POST http://localhost:8080/admin/flows/dry-run \
  -H "Content-Type: application/json" \
  -d '{
    "env": "development",
    "flowId": "payment-check-flow",
    "input": {
      "method": "POST",
      "path": "/check",
      "body": { "payment_status": "PAID", "amount": 100 }
    }
  }'
```

Response shows which branch was taken:

```json
{
  "trace_id": "dry-123",
  "dry_run": true,
  "steps": [
    { "node_id": "check-status", "node_type": "condition", "branch_taken": "paid-branch" },
    { "node_id": "paid-branch", "node_type": "set" }
  ]
}
```

---

## Best Practices

1. **Validate before storing** — Always validate flow structure before publishing.

2. **Use fixtures for regression** — Define fixtures for common test cases.

3. **Dry-run before production** — Test with realistic input in development first.

4. **Check branch coverage** — Dry-run with inputs that exercise different branches.

5. **Review trace for unexpected paths** — Look for unexpected branch_taken values.

6. **Test error handling** — Dry-run with invalid input to verify error responses.

7. **Document expected behavior** — Fixtures serve as executable documentation.
