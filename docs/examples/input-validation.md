# Input Validation — JSON Schema validation for requests

The rule engine validates incoming requests using JSON Schema defined in the
`trigger.spec.input.schema` field. Validation runs before the flow executes,
returning a 400 error with detailed field-level messages on failure.

**Related architecture:** [slice-a-interpreter.md](../architecture/lld/slice-a-interpreter.md)

---

## Input Extraction

The `TriggerInput` spec declares which parts of the request are extracted:

```json
{
  "input": {
    "params": ["order_id", "customer_id"],
    "query": ["page", "limit", "sort"],
    "headers": ["X-Request-ID", "X-Tenant-ID", "Authorization"],
    "body": true,
    "schema": { /* JSON Schema */ }
  }
}
```

| Field     | Type     | Description                                           |
|-----------|----------|-------------------------------------------------------|
| `params`  | []string | Path parameters to extract (e.g., `{order_id}`)       |
| `query`   | []string | Query parameters to extract                           |
| `headers` | []string | Headers to extract (case-insensitive lookup)          |
| `body`    | bool     | Whether to parse JSON body into `body.*`              |
| `schema`  | object   | JSON Schema to validate the extracted input           |

### How Input Is Structured

Extracted values are placed in a flat map that the schema validates:

```json
{
  "order_id": "ORD-123",
  "customer_id": "CUST-456",
  "page": "1",
  "limit": "20",
  "X-Request-ID": "req-abc-123",
  "X-Tenant-ID": "tenant-1",
  "body": {
    "amount": 99.99,
    "items": [{ "sku": "ITEM-1", "qty": 2 }]
  }
}
```

Note: Path params and query params are always strings. The schema can coerce or
validate types as needed.

---

## JSON Schema Validation

### Basic Body Validation

Validate required fields and types in the request body:

```json
{
  "type": "trigger",
  "spec": {
    "method": "POST",
    "path": "/orders",
    "input": {
      "body": true,
      "schema": {
        "type": "object",
        "required": ["body"],
        "properties": {
          "body": {
            "type": "object",
            "required": ["customer_id", "items"],
            "properties": {
              "customer_id": {
                "type": "string",
                "minLength": 1
              },
              "items": {
                "type": "array",
                "minItems": 1,
                "items": {
                  "type": "object",
                  "required": ["sku", "quantity"],
                  "properties": {
                    "sku": { "type": "string" },
                    "quantity": { "type": "integer", "minimum": 1 }
                  }
                }
              },
              "notes": { "type": "string" }
            },
            "additionalProperties": false
          }
        }
      }
    }
  }
}
```

**Valid request:**
```json
POST /orders
Content-Type: application/json

{
  "customer_id": "CUST-123",
  "items": [
    { "sku": "PROD-A", "quantity": 2 },
    { "sku": "PROD-B", "quantity": 1 }
  ]
}
```

**Invalid request (missing required field):**
```json
POST /orders
Content-Type: application/json

{
  "items": [{ "sku": "PROD-A", "quantity": 2 }]
}
```

**Error response:**
```json
{
  "error": "validation failed",
  "details": [
    {
      "field": "body.customer_id",
      "code": "required",
      "message": "missing required property"
    }
  ]
}
```

---

### Path Parameter Validation

Validate path parameters with patterns:

```json
{
  "type": "trigger",
  "spec": {
    "method": "GET",
    "path": "/orders/{order_id}",
    "input": {
      "params": ["order_id"],
      "schema": {
        "type": "object",
        "required": ["order_id"],
        "properties": {
          "order_id": {
            "type": "string",
            "pattern": "^ORD-[A-Z0-9]{8}$"
          }
        }
      }
    }
  }
}
```

**Valid:** `GET /orders/ORD-ABC12345`
**Invalid:** `GET /orders/123` → 400 with pattern mismatch error

---

### Query Parameter Validation

Validate and constrain query parameters:

```json
{
  "type": "trigger",
  "spec": {
    "method": "GET",
    "path": "/orders",
    "input": {
      "query": ["page", "limit", "status", "sort"],
      "schema": {
        "type": "object",
        "properties": {
          "page": {
            "type": "string",
            "pattern": "^[0-9]+$"
          },
          "limit": {
            "type": "string",
            "pattern": "^[0-9]+$"
          },
          "status": {
            "type": "string",
            "enum": ["pending", "processing", "completed", "cancelled"]
          },
          "sort": {
            "type": "string",
            "enum": ["created_asc", "created_desc", "updated_asc", "updated_desc"]
          }
        }
      }
    }
  }
}
```

**Valid:** `GET /orders?page=1&limit=20&status=pending`
**Invalid:** `GET /orders?status=invalid` → 400 with enum mismatch

---

### Header Validation

Validate required headers:

```json
{
  "type": "trigger",
  "spec": {
    "method": "POST",
    "path": "/orders",
    "input": {
      "headers": ["X-Tenant-ID", "X-Idempotency-Key"],
      "body": true,
      "schema": {
        "type": "object",
        "required": ["X-Tenant-ID", "X-Idempotency-Key", "body"],
        "properties": {
          "X-Tenant-ID": {
            "type": "string",
            "pattern": "^[a-z0-9-]+$",
            "minLength": 1
          },
          "X-Idempotency-Key": {
            "type": "string",
            "format": "uuid"
          },
          "body": { "type": "object" }
        }
      }
    }
  }
}
```

**Valid request:**
```
POST /orders
X-Tenant-ID: acme-corp
X-Idempotency-Key: 550e8400-e29b-41d4-a716-446655440000
Content-Type: application/json

{"customer_id": "CUST-123"}
```

**Invalid (missing header):**
```
POST /orders
Content-Type: application/json

{"customer_id": "CUST-123"}
```

**Error response:**
```json
{
  "error": "validation failed",
  "details": [
    {
      "field": "X-Tenant-ID",
      "code": "required",
      "message": "missing required property"
    }
  ]
}
```

---

### Combined Validation Example

Full example validating path params, query, headers, and body together:

```json
{
  "id": "update-order-trigger",
  "type": "trigger",
  "spec": {
    "method": "PUT",
    "path": "/orders/{order_id}",
    "input": {
      "params": ["order_id"],
      "query": ["notify"],
      "headers": ["X-Request-ID", "X-Tenant-ID"],
      "body": true,
      "schema": {
        "type": "object",
        "required": ["order_id", "X-Tenant-ID", "body"],
        "properties": {
          "order_id": {
            "type": "string",
            "pattern": "^ORD-[A-Z0-9]+$"
          },
          "notify": {
            "type": "string",
            "enum": ["true", "false"]
          },
          "X-Request-ID": {
            "type": "string"
          },
          "X-Tenant-ID": {
            "type": "string",
            "minLength": 1
          },
          "body": {
            "type": "object",
            "required": ["status"],
            "properties": {
              "status": {
                "type": "string",
                "enum": ["processing", "shipped", "delivered", "cancelled"]
              },
              "tracking_number": {
                "type": "string"
              },
              "notes": {
                "type": "string",
                "maxLength": 500
              }
            },
            "additionalProperties": false
          }
        }
      }
    }
  },
  "children": [
    { "id": "update-db", "type": "action", "spec": { /* ... */ } },
    { "id": "respond", "type": "response", "spec": { /* ... */ } }
  ]
}
```

---

## Validation Error Response

When validation fails, the engine returns a 400 status with structured errors:

```json
{
  "error": "validation failed: 2 errors",
  "details": [
    {
      "field": "body.status",
      "code": "enum",
      "message": "value must be one of: processing, shipped, delivered, cancelled"
    },
    {
      "field": "order_id",
      "code": "pattern",
      "message": "string doesn't match pattern '^ORD-[A-Z0-9]+$'"
    }
  ]
}
```

### Error Codes

| Code         | Description                                |
|--------------|--------------------------------------------|
| `required`   | Required property is missing               |
| `type`       | Value has wrong type                       |
| `enum`       | Value not in allowed set                   |
| `pattern`    | String doesn't match regex pattern         |
| `minLength`  | String too short                           |
| `maxLength`  | String too long                            |
| `minimum`    | Number below minimum                       |
| `maximum`    | Number above maximum                       |
| `minItems`   | Array has too few items                    |
| `maxItems`   | Array has too many items                   |
| `format`     | Value doesn't match format (uuid, email)   |
| `additionalProperties` | Unknown field present             |

---

## Best Practices

1. **Always validate body for POST/PUT/PATCH** — Set `"body": true` and include
   body validation in the schema.

2. **Use patterns for IDs** — Validate ID formats early to catch typos:
   ```json
   "order_id": { "type": "string", "pattern": "^ORD-[A-Z0-9]{8}$" }
   ```

3. **Require tenant headers in multi-tenant systems** — Fail fast if tenant
   context is missing:
   ```json
   "required": ["X-Tenant-ID"]
   ```

4. **Use `additionalProperties: false`** — Reject unknown fields to catch
   client bugs and prevent data injection:
   ```json
   "additionalProperties": false
   ```

5. **Validate enums at the edge** — Catch invalid status/type values before
   they reach business logic:
   ```json
   "status": { "type": "string", "enum": ["active", "inactive"] }
   ```

6. **Keep schemas simple** — Complex nested validation may be better handled
   by a dedicated validation node in the flow rather than at the trigger level.
