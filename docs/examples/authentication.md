# Authentication Reference — AuthN and AuthZ configuration

The rule engine provides JWT/JWKS authentication for public APIs and ZEN-based
authorization for fine-grained access control. Admin/operator endpoints use static
token authentication. This guide covers all authentication and authorization patterns.

**Related architecture:** [slice-c-zen-auth.md](../architecture/lld/slice-c-zen-auth.md)

## Overview

Authentication flow:

```
Request → Authn Middleware → Authz Middleware → Handler
              │                    │
              └→ 401 Unauthorized  └→ 403 Forbidden
```

The middleware chain:
1. **Authn**: Extracts and validates Bearer token, injects Principal
2. **Authz**: Reads Principal, evaluates ZEN decision, allows or denies

Deny-by-default: missing/invalid token → 401; denied by policy → 403.

---

## Public API Authentication (JWT/JWKS)

### Authenticator Interface

```go
type Authenticator interface {
    Authenticate(ctx context.Context, bearer string) (Principal, error)
}
```

### Principal

On successful authentication, a Principal is injected into the context:

```json
{
  "subject": "user-123",
  "roles": ["admin", "viewer"],
  "claims": {
    "email": "user@example.com",
    "org_id": "org-456"
  }
}
```

| Field     | Type            | Description                        |
|-----------|-----------------|------------------------------------|
| `subject` | string          | Unique user identifier (JWT `sub`) |
| `roles`   | []string        | User roles from token claims       |
| `claims`  | map[string]any  | Additional JWT claims              |

### JWT Validation

The authenticator validates:
- Token signature against JWKS endpoint
- Token expiration (`exp` claim)
- Issuer (`iss` claim) if configured
- Audience (`aud` claim) if configured

### Request Example

```sh
curl http://localhost:8080/orders/123 \
  -H "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."
```

---

## Authorization (ZEN-based)

### Authorizer Interface

```go
type Authorizer interface {
    Authorize(ctx context.Context, in AuthzInput) (Decision, error)
}
```

### AuthzInput

The authorization input passed to the ZEN decision:

```json
{
  "user": "user-123",
  "roles": ["admin", "viewer"],
  "resource": "/orders/123",
  "action": "read",
  "attrs": {
    "org_id": "org-456",
    "department": "sales"
  }
}
```

| Field      | Type            | Description                        |
|------------|-----------------|------------------------------------|
| `user`     | string          | User identifier (Principal.Subject)|
| `roles`    | []string        | User roles                         |
| `resource` | string          | Resource being accessed (URL path) |
| `action`   | string          | Action being performed             |
| `attrs`    | map[string]any  | Additional ABAC attributes         |

### Decision

The authorization decision:

```json
{
  "allow": true,
  "reason": "user has admin role"
}
```

| Field    | Type   | Description                    |
|----------|--------|--------------------------------|
| `allow`  | bool   | Whether access is granted      |
| `reason` | string | Explanation (for audit logs)   |

### Action Mapping

HTTP methods map to actions:

| Method  | Action   |
|---------|----------|
| GET     | `read`   |
| POST    | `create` |
| PUT     | `update` |
| PATCH   | `update` |
| DELETE  | `delete` |

---

## ZEN Authorization Policy

Create authorization policies as ZEN decision tables.

### Role-Based Access Control (RBAC)

Simple role-based policy:

| roles (contains) | action | resource (prefix) | allow | reason                |
|------------------|--------|-------------------|-------|-----------------------|
| "admin"          | *      | *                 | true  | "admin has full access" |
| "editor"         | "read" | *                 | true  | "editor can read"     |
| "editor"         | "create" | /orders         | true  | "editor can create orders" |
| "editor"         | "update" | /orders         | true  | "editor can update orders" |
| "viewer"         | "read" | *                 | true  | "viewer can read"     |
| *                | *      | *                 | false | "default deny"        |

### Attribute-Based Access Control (ABAC)

More complex policy with attributes:

| roles (contains) | attrs.org_id | resource (regex) | action | allow | reason                         |
|------------------|--------------|------------------|--------|-------|--------------------------------|
| "admin"          | *            | *                | *      | true  | "admin full access"            |
| "manager"        | =user.org_id | /orders/.*       | *      | true  | "manager access own org orders"|
| "user"           | =user.org_id | /orders/.*       | "read" | true  | "user read own org orders"     |
| *                | *            | *                | *      | false | "default deny"                 |

---

## Operator/Admin Authentication

Admin endpoints use static token authentication for operators.

### Token Format

Token specification: `sha256hex:subject:comma,roles`

```
ADMIN_TOKENS='abc123def456...:op:alice:admin,editor;def789ghi012...:op:bob:viewer'
```

Where:
- `abc123def456...`: SHA256 hash of the actual token (hex-encoded)
- `op:alice`: Subject identifier
- `admin,editor`: Comma-separated roles

### Environment Configuration

```sh
# Enable admin plane
export ADMIN_ENABLED=true

# Set admin tokens (semicolon-separated)
export ADMIN_TOKENS='<sha256>:op:alice:admin;<sha256>:op:bob:viewer'
```

### Request Example

```sh
curl http://localhost:8080/admin/flows \
  -H "Authorization: Bearer actual-token-value"
```

### Generating Token Hashes

```sh
# Generate a random token and its hash
TOKEN=$(openssl rand -hex 32)
HASH=$(echo -n "$TOKEN" | sha256sum | cut -d' ' -f1)
echo "Token: $TOKEN"
echo "Hash: $HASH"
echo "Spec: ${HASH}:op:alice:admin"
```

### Admin Disabled Behavior

If `ADMIN_ENABLED=false` or unset, all admin routes return:

```
HTTP/1.1 503 Service Unavailable
{
  "error": "admin plane disabled"
}
```

---

## Operator Guard

Admin endpoints use the operator guard middleware:

```
Request → Authn → Require Roles → Handler
            │           │
            └→ 401      └→ 403
```

### Role Requirements by Endpoint

| Endpoint Pattern           | Required Role |
|----------------------------|---------------|
| `GET /admin/*`             | viewer+       |
| `POST /admin/flows`        | editor+       |
| `PUT /admin/flows/*`       | editor+       |
| `DELETE /admin/flows/*`    | admin         |
| `POST /admin/flows/dry-run`| editor+       |

---

## Request/Response Examples

### Successful Authentication

```sh
curl http://localhost:8080/orders/123 \
  -H "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."
```

Response:
```json
{
  "order_id": "123",
  "status": "confirmed"
}
```

### Missing Token (401)

```sh
curl http://localhost:8080/orders/123
```

Response:
```
HTTP/1.1 401 Unauthorized
{
  "error": "missing authorization header"
}
```

### Invalid Token (401)

```sh
curl http://localhost:8080/orders/123 \
  -H "Authorization: Bearer invalid-token"
```

Response:
```
HTTP/1.1 401 Unauthorized
{
  "error": "invalid token"
}
```

### Authorization Denied (403)

```sh
curl -X DELETE http://localhost:8080/orders/123 \
  -H "Authorization: Bearer eyJ..."  # viewer role
```

Response:
```
HTTP/1.1 403 Forbidden
{
  "error": "access denied",
  "reason": "viewer role cannot delete"
}
```

---

## Complete Example: Flow with AuthZ

A flow that checks authorization:

```json
{
  "id": "get-order",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": {
      "method": "GET",
      "path": "/orders/{order_id}",
      "input": {
        "params": ["order_id"],
        "headers": ["Authorization"]
      }
    },
    "children": [
      {
        "id": "seq-main",
        "type": "sequence",
        "spec": { "stopOnError": true },
        "children": [
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
              "unwrapSingleRow": true
            }
          },
          {
            "id": "check-access",
            "type": "condition",
            "spec": {
              "jdmId": "order-access-policy",
              "input": ["principal.subject", "principal.roles", "order.org_id"],
              "trueKey": "allow-access",
              "falseKey": "deny-access",
              "branchField": "allow"
            },
            "children": [
              {
                "id": "allow-access",
                "type": "set",
                "spec": {
                  "targetPath": "response",
                  "from": "order"
                }
              },
              {
                "id": "deny-access",
                "type": "response",
                "spec": {
                  "status": 403,
                  "bodyFrom": "{{\"error\": \"access denied\"}}"
                }
              }
            ]
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

---

## Best Practices

1. **Always use HTTPS** — Never transmit tokens over unencrypted connections.

2. **Rotate secrets regularly** — Rotate JWKS keys and admin tokens periodically.

3. **Use short token lifetimes** — JWT tokens should expire within minutes/hours, not days.

4. **Implement least privilege** — Grant minimum necessary roles/permissions.

5. **Audit authorization decisions** — Log all authz decisions with reasons for security review.

6. **Test policies with dry-run** — Validate authorization policies before production deployment.

7. **Handle errors gracefully** — Return appropriate HTTP status codes (401, 403) without leaking details.

8. **Disable admin in production** — If admin plane isn't needed, keep `ADMIN_ENABLED=false`.
