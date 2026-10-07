# TASK-009: Multi-Secret References for Connections — Requirements

## Summary

Extend the connection model to support multiple named secrets per connection, replacing the single `secretRef` string with a `secretRefs` map. This enables connectors that need multiple credentials (Kafka SASL, REST with multiple auth headers, mTLS certs) while maintaining backward compatibility with existing single-secret connections.

## Background and Motivation

The current architecture limits each connection to a single secret reference (`secretRef: string`), typically used as a password. Real-world integrations require multiple credentials:

| Connector  | Current     | Needed                                      |
|------------|-------------|---------------------------------------------|
| postgres   | password ✓  | sslCert, sslKey (mTLS)                     |
| mysql      | password ✓  | sslCert, sslKey                            |
| valkey     | password ✓  | username (ACL), tlsCert                    |
| rest       | ❌ limited   | Any headers (Authorization, X-API-Key, etc.) |
| kafka      | password ✓  | username (SASL), sslCert, sslKey, sslCA    |
| rabbitmq   | password ✓  | username, clientCert, clientKey            |

REST is the urgent case — it's fundamentally multi-secret since users define which headers require secrets.

## Existing Architecture

Key files identified:
- **ConnectionDef**: `engine/internal/connect/connect.go` — currently has `SecretRef string`
- **Connector interface**: `engine/internal/connect/connector.go` — defines `Type()`, `Open()`, `Lifecycle()`, `Capabilities()`
- **Registry**: `engine/internal/connect/registry.go` — resolves secrets via `open()`, uses `WithSecret(ctx, sec)` to pass single secret
- **Secret helpers**: `engine/internal/connect/connector.go` — `WithSecret(ctx, Secret)` and `SecretFrom(ctx)`
- **SecretProvider**: `engine/internal/connect/secret.go` — resolves `env:NAME` refs to `Secret` values
- **Test endpoint**: `engine/internal/httpapi/admin_handlers.go` — accepts single `secret` string
- **CMS schema**: `cms/src/api/connection/content-types/connection/schema.json` — `secretRef` as string
- **CMS TestConnectionModal**: `cms/src/plugins/rule-engine/admin/src/components/TestConnectionModal/index.tsx` — single password field

---

## Functional Requirements

### Part 1: Engine Data Model

**FR-1.1** Add `SecretRefs map[string]string` field to `ConnectionDef` struct in `engine/internal/connect/connect.go`.

**FR-1.2** Retain existing `SecretRef string` field for backward compatibility.

**FR-1.3** Add context helper `WithSecrets(ctx context.Context, secrets map[string]Secret) context.Context` in `engine/internal/connect/connector.go`.

**FR-1.4** Add context helper `SecretsFrom(ctx context.Context) (map[string]Secret, bool)` in `engine/internal/connect/connector.go`.

**FR-1.5** The existing `WithSecret`/`SecretFrom` helpers remain unchanged for drivers that only need one secret.

### Part 2: Engine Registry

**FR-2.1** Update `registry.open()` in `engine/internal/connect/registry.go` to resolve multiple secrets from `SecretRefs` map.

**FR-2.2** If only legacy `SecretRef` is set (and `SecretRefs` is empty/nil), treat as `{"password": ref}` — resolve and pass as `secrets["password"]`.

**FR-2.3** Pass resolved secrets map to connector via `WithSecrets(ctx, secrets)` before calling `connector.Open()`.

**FR-2.4** For backward compat, also call `WithSecret(ctx, secrets["password"])` if a "password" key exists so existing driver code continues to work.

**FR-2.5** Update `registryV2` (lazy pool registry) with the same logic if `USE_CONNECTOR_POOL=true` is used.

### Part 3: Connector Interface

**FR-3.1** Add optional method `SecretSchema() []SecretField` to the `Connector` interface. Connectors that don't implement it are treated as having no schema (dynamic secrets).

**FR-3.2** Define `SecretField` struct:
```go
type SecretField struct {
    Name     string // key name, e.g. "password", "username", "clientCert"
    Required bool   // whether this secret must be provided
    Label    string // human-readable label for UI
}
```

**FR-3.3** Each connector declares its schema:
- **postgres**: `[{Name: "password", Required: false, Label: "Database Password"}]`
- **mysql**: `[{Name: "password", Required: false, Label: "Database Password"}]`
- **valkey**: `[{Name: "password", Required: false, Label: "Redis Password"}]`
- **kafka**: `[{Name: "password", Required: false, Label: "SASL Password"}, {Name: "username", Required: false, Label: "SASL Username"}]`
- **rabbitmq**: `[{Name: "password", Required: false, Label: "Password"}, {Name: "username", Required: false, Label: "Username"}]`
- **rest/http**: returns `nil` (dynamic — user defines which headers need secrets)

**FR-3.4** Update each connector's `Open()` to read from `SecretsFrom(ctx)` map instead of only `SecretFrom(ctx)`. Fall back to `SecretFrom` for backward compat if secrets map is empty.

### Part 4: Schema Endpoint

**FR-4.1** Add `GET /admin/connectors/schema` endpoint returning all connector schemas:
```json
{
  "connectors": {
    "postgres": { "secrets": [{"name": "password", "required": false, "label": "Database Password"}] },
    "kafka": { "secrets": [...] },
    "rest": { "secrets": null }
  }
}
```

**FR-4.2** Optionally add `GET /admin/connectors/:type/schema` for a single connector's schema.

**FR-4.3** The endpoint is read-only, no auth required beyond existing admin middleware.

### Part 5: Test Connection Endpoint

**FR-5.1** Update `POST /admin/connections/test` to accept `secrets` map alongside (or instead of) the legacy `secret` string:
```json
{
  "type": "kafka",
  "settings": { "brokers": "kafka:9092", "topic": "events" },
  "secrets": {
    "username": "kafka_user",
    "password": "kafka_pass"
  }
}
```

**FR-5.2** For backward compat, if only `secret` (string) is provided, treat as `{"password": secret}`.

**FR-5.3** Build the secrets map, call `WithSecrets(ctx, map)` before `connector.Open()`.

### Part 6: CMS Schema

**FR-6.1** Add `secretRefs` field to the connection content-type schema as JSON type:
```json
"secretRefs": {
  "type": "json"
}
```

**FR-6.2** Retain `secretRef` (string) for backward compat with existing connections.

**FR-6.3** When syncing to engine:
- If `secretRefs` JSON is set and non-empty, use it as the `SecretRefs` map
- Else if `secretRef` string is set, pass as legacy `SecretRef`
- Else no secrets

### Part 7: CMS Admin UI

**FR-7.1** On the Connections page, update the form to show a dynamic secret refs component:
- For fixed-schema connectors (postgres, kafka, etc.): render labeled fields based on schema from engine
- For REST: render a repeatable key-value pair component (header name → secret ref)

**FR-7.2** Query `GET /admin/connectors/schema` from engine to get connector schemas on page load.

**FR-7.3** Update `TestConnectionModal` to:
- Show multiple secret input fields based on connector schema
- For REST: show a repeatable row for each secret header the user defined in the connection
- Send `secrets` map instead of single `secret` string to test endpoint

### Part 8: Database Storage

**FR-8.1** No database schema migration required. The existing `secret_ref` column (if JSONB) can store either:
- A string (legacy single secret): `"env:PG_PASSWORD"`
- An object (multi-secret map): `{"password": "env:X", "username": "env:Y"}`

**FR-8.2** When reading from DB, parse smartly:
- If value is a string → legacy single secret (`SecretRef`)
- If value is an object → multi-secret map (`SecretRefs`)

**FR-8.3** If the column is currently VARCHAR, it stores only the legacy string. The CMS stores `secretRefs` in a separate JSON field; the engine receives the structured form at sync time.

---

## Non-Functional Requirements

**NFR-1** No breaking changes to existing connections. A connection with only `secretRef` set continues to work without modification.

**NFR-2** Secrets are never logged, persisted, or included in error messages. Only refs (e.g., "env:PG_PASSWORD") appear in logs.

**NFR-3** The schema endpoint response is cacheable for UI — connector schemas are static at runtime.

**NFR-4** Test endpoint remains stateless — secrets are used only for the transient test, never stored.

---

## Acceptance Criteria

### Part 1: Engine Data Model
- **AC-1.1** `ConnectionDef` struct has both `SecretRef string` and `SecretRefs map[string]string` fields.
- **AC-1.2** `WithSecrets(ctx, map)` and `SecretsFrom(ctx)` helpers exist and work correctly.
- **AC-1.3** Unit tests prove both helpers round-trip the secrets map through context.

### Part 2: Engine Registry
- **AC-2.1** A connection with `SecretRefs: {"password": "env:X", "username": "env:Y"}` resolves both secrets and passes them to the connector.
- **AC-2.2** A connection with only `SecretRef: "env:X"` (no `SecretRefs`) is treated as `{"password": "env:X"}`.
- **AC-2.3** A connection with both set prefers `SecretRefs` over `SecretRef`.
- **AC-2.4** Unit test confirms `open()` resolves multiple secrets and calls `WithSecrets`.

### Part 3: Connector Interface
- **AC-3.1** `SecretSchema() []SecretField` method exists on `Connector` interface (or as optional method pattern).
- **AC-3.2** Each connector (postgres, mysql, valkey, kafka, rabbitmq, rest) implements `SecretSchema()`.
- **AC-3.3** Postgres connector reads password from `SecretsFrom(ctx)["password"]`, falls back to `SecretFrom(ctx)`.
- **AC-3.4** Unit tests confirm each connector's Open works with secrets map.

### Part 4: Schema Endpoint
- **AC-4.1** `GET /admin/connectors/schema` returns JSON with all connector schemas.
- **AC-4.2** REST connector's schema is `null` (dynamic).
- **AC-4.3** Integration test verifies endpoint returns expected structure.

### Part 5: Test Connection Endpoint
- **AC-5.1** `POST /admin/connections/test` accepts `{"type": "kafka", "settings": {...}, "secrets": {"username": "u", "password": "p"}}`.
- **AC-5.2** Legacy format `{"type": "postgres", "settings": {...}, "secret": "pass"}` continues to work.
- **AC-5.3** Unit test confirms multi-secret test request succeeds.

### Part 6: CMS Schema
- **AC-6.1** Connection content-type has `secretRefs` (JSON) field alongside `secretRef` (string).
- **AC-6.2** Existing connections with only `secretRef` continue to display and function.
- **AC-6.3** A new connection can be created with `secretRefs` as a key-value map.

### Part 7: CMS Admin UI
- **AC-7.1** Connections page renders dynamic secret refs form based on connector type.
- **AC-7.2** REST connections show a repeatable key-value component for secret headers.
- **AC-7.3** Test modal shows multiple secret input fields matching the connector schema.
- **AC-7.4** Test modal for REST shows fields for each user-defined secret header.

### Part 8: Database Storage
- **AC-8.1** Engine reads a string `secret_ref` column as legacy single secret.
- **AC-8.2** Engine reads a JSON object `secret_ref` column as multi-secret map.
- **AC-8.3** No migration is required; existing data continues to work.

---

## Out of Scope

1. **Vault/SSM integration** — `SecretProvider` currently only supports `env:` scheme. Other providers are a separate task.
2. **Secret rotation/Watch** — `SecretProvider.Watch` exists but is a no-op for env provider. Rotation is not addressed here.
3. **Per-operation secret override** — Secrets are connection-level, not operation-level.
4. **mTLS certificate file handling** — Secrets store the cert/key content as strings; mounting as files is connector-specific and not part of this task.
5. **Migration of existing connections** — Existing single-secret connections work via backward compat; no automatic migration to multi-secret format.

---

## Assumptions

1. The `secret_ref` column in the engine database is JSONB or the engine receives structured `SecretRefs` map from CMS sync (not raw DB read).
2. Connectors that need mTLS certs will receive cert content as string values in the secrets map and handle file materialization internally.
3. REST connector's "dynamic secrets" are header values — the user defines which headers need secret values via the `secretRefs` map keys.
