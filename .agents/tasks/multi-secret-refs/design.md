# TASK-009: Multi-Secret References for Connections — Technical Design

## Overview

This design extends the connection system to support multiple named secrets per connection. The current single `SecretRef string` field limits connectors to one credential, but real-world integrations (Kafka SASL, REST with multiple auth headers, mTLS) require multiple secrets. The design introduces `SecretRefs map[string]string` alongside the existing field, adds a `SecretSchema()` method to connectors (via an optional interface) so connectors declare their expected secrets, and updates the registry, test endpoint, and CMS to resolve and present multiple secrets.

REST is the primary driver: it needs arbitrary header secrets whose names the user defines. Fixed-schema connectors (postgres, mysql, valkey) declare their known fields; REST returns `nil` to signal "dynamic, user-defined keys." Kafka and RabbitMQ require implementation work to actually use secrets — they currently don't read from context.

---

## 1. Engine Data Model

### 1.1 ConnectionDef Changes

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/connect/connect.go`

Add `SecretRefs` field to `ConnectionDef`:

```go
type ConnectionDef struct {
    Key, Type  string
    Settings   map[string]any
    SecretRef  string             // Legacy single secret (kept for backward compat)
    SecretRefs map[string]string  // NEW: named secret refs, e.g. {"password": "env:X", "username": "env:Y"}
    Resilience ResiliencePolicy
}
```

**Frozen contract acknowledgment:** `ConnectionDef` is described in the package comment (lines 7-11) as part of the frozen contract. Adding `SecretRefs` is an **additive change** — existing code that ignores the new field continues to work unchanged. This is a safe extension: no method signatures change, no existing consumers break. The field is optional (zero value = empty map = no change in behavior).

**Migration path:** No DB migration required. The existing `secretRef` column in CMS Strapi stores a string. The new `secretRefs` field is stored in a separate JSON column (`secretRefs`). When syncing to the engine:
- If `secretRefs` is set and non-empty → use it as `SecretRefs` map
- Else if `secretRef` string is set → use legacy `SecretRef` string  
- Else → no secrets

The engine never reads raw DB storage directly; it receives structured `ConnectionDef` from CMS sync. No parsing of JSONB string vs object is needed at the engine layer.

### 1.2 Context Helpers

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/connect/connector.go`

Add new context helpers alongside existing ones:

```go
// secretsCtxKey carries resolved secrets map from registry to Connector.Open
type secretsCtxKey struct{}

// WithSecrets returns a context carrying multiple resolved secrets for Open.
func WithSecrets(ctx context.Context, secrets map[string]Secret) context.Context {
    return context.WithValue(ctx, secretsCtxKey{}, secrets)
}

// SecretsFrom returns the resolved secrets map placed on ctx by the registry.
// A driver Open calls this to obtain credentials. Returns nil map and false if not set.
func SecretsFrom(ctx context.Context) (map[string]Secret, bool) {
    s, ok := ctx.Value(secretsCtxKey{}).(map[string]Secret)
    return s, ok
}
```

**Existing helpers unchanged:** `WithSecret` and `SecretFrom` remain for backward compatibility. Connectors that only need one secret continue using `SecretFrom`.

### 1.3 How Legacy Single-Secret Maps to Multi-Secret

When the registry sees only `SecretRef` (no `SecretRefs`):
1. Resolve `SecretRef` to a `Secret`
2. Call `WithSecrets(ctx, map[string]Secret{"password": secret})`
3. Also call `WithSecret(ctx, secret)` for backward compat

This ensures existing connector code using `SecretFrom(ctx)` continues to work without modification.

---

## 2. Connector Interface Changes

### 2.1 SecretSchema Method

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/connect/connector.go`

Add optional method via interface assertion pattern (no breaking change to existing interface):

```go
// SecretField describes one expected secret for a connector type.
type SecretField struct {
    Name     string `json:"name"`     // key name: "password", "username", "clientCert"
    Required bool   `json:"required"` // true if this secret must be provided
    Label    string `json:"label"`    // human-readable label for UI: "Database Password"
}

// SecretSchemaProvider is the optional interface connectors implement to declare
// their expected secrets. Connectors that don't implement it are treated as
// having dynamic/user-defined secrets (REST).
type SecretSchemaProvider interface {
    // SecretSchema returns the list of expected secrets. Returning nil signals
    // dynamic secrets (the user defines which keys exist via secretRefs map).
    SecretSchema() []SecretField
}
```

**Design choice — optional interface vs method on Connector:**  
Using a separate optional interface (`SecretSchemaProvider`) avoids modifying the existing `Connector` interface, which would require updating all existing connectors even if they don't need multi-secret support. Connectors opt in by implementing the additional interface.

### 2.2 Per-Connector Impact

Each connector gets a `SecretSchema()` method. Existing connectors already satisfy `Connector`; adding `SecretSchemaProvider` is additive:

| Connector | SecretSchema Implementation | Current State | Required Work |
|-----------|----------------------------|---------------|---------------|
| **postgres** | `[{Name: "password", Required: false, Label: "Database Password"}]` | ✅ Uses `SecretFrom(ctx)` for password (line 43-45 in drivers/postgres.go) | Add `SecretSchema()`, update `Open()` to prefer `SecretsFrom` |
| **mysql** | `[{Name: "password", Required: false, Label: "Database Password"}]` | ✅ Uses `SecretFrom(ctx)` for password (line 40-42 in drivers/mysql.go) | Add `SecretSchema()`, update `Open()` to prefer `SecretsFrom` |
| **valkey** | `[{Name: "password", Required: false, Label: "Redis Password"}]` | ✅ Uses `SecretFrom(ctx)` for password (line 53-55 in drivers/valkey.go) | Add `SecretSchema()`, update `Open()` to prefer `SecretsFrom` |
| **kafka** | `[{Name: "password", Required: false, Label: "SASL Password"}, {Name: "username", Required: false, Label: "SASL Username"}]` | ❌ **Does NOT use secrets** — no `SecretFrom` call anywhere | Add `SecretSchema()`, add SASL auth support in `Open()` |
| **rabbitmq** | `[{Name: "password", Required: false, Label: "Password"}, {Name: "username", Required: false, Label: "Username"}]` | ❌ **Stub only** — reads secret (line 57-60) but ignores it | Add `SecretSchema()`, implement URL credential injection or discrete fields |
| **rest/http** | returns `nil` | ❌ **Does NOT use secrets** — no `SecretFrom` call | Add `SecretSchema()` returning nil, add secret-as-header support in `Open()` |

**Note:** Both `rest` and `http` types use the same `restConnector` implementation. Registration in `drivers/registry.go` creates two instances:
```go
connectors := []connect.Connector{
    newRESTConnector("rest"),
    newRESTConnector("http"),
    // ...
}
```
Both return `nil` from `SecretSchema()` since the same struct backs both aliases.

### 2.3 Connector Open() Updates — Pattern

Each connector's `Open()` method is updated to read from `SecretsFrom(ctx)` with fallback to `SecretFrom(ctx)`:

```go
func (pgConnector) Open(ctx context.Context, def ConnectionDef) (Client, error) {
    // ... existing config parsing ...

    // Try multi-secret first, fall back to single secret
    if secrets, ok := SecretsFrom(ctx); ok {
        if sec, exists := secrets["password"]; exists && !sec.IsZero() {
            cfg.ConnConfig.Password = string(sec.Reveal())
        }
    } else if sec, ok := SecretFrom(ctx); ok && !sec.IsZero() {
        cfg.ConnConfig.Password = string(sec.Reveal())
    }

    // ... rest of Open ...
}
```

### 2.4 REST Connector — Handling Dynamic Secrets

**Current behavior:** REST connector ignores secrets entirely — the existing `Open()` in `drivers/rest.go` does NOT call `SecretFrom(ctx)`. The `restClient` stores only headers from `Settings["headers"]` (static config).

**New behavior:** REST reads `SecretsFrom(ctx)` map. Each entry's key is treated as an HTTP header name and its resolved secret value becomes the header value. This is additive to (and overrides) static headers from settings.

**Design decision:** Secret ref keys for REST are header names. The user sets `secretRefs: {"Authorization": "env:TOKEN"}`, and the resolved value becomes the `Authorization` header. This is intuitive — the key names what it's for, the value is the secret reference.

**Implementation:**

```go
func (c restConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
    // ... existing setup (baseURL, transport) ...

    defaultHeaders := stringMapSetting(def.Settings, "headers")
    // Initialize map before secrets loop to avoid nil map assignment
    if defaultHeaders == nil {
        defaultHeaders = make(map[string]string)
    }

    // Inject resolved secrets as header values
    // Secret keys are header names; resolved secret values become header values
    if secrets, ok := SecretsFrom(ctx); ok {
        for headerName, sec := range secrets {
            if !sec.IsZero() {
                defaultHeaders[headerName] = string(sec.Reveal())  // Secrets override static headers
            }
        }
    }

    return &restClient{
        key:            def.Key,
        baseURL:        strings.TrimRight(baseURL, "/"),
        defaultHeaders: defaultHeaders,
        httpClient:     &http.Client{Transport: tr},
    }, nil
}
```

### 2.5 Kafka Connector — SASL Authentication Support

**Current state:** Kafka connector does NOT support SASL authentication (no secret handling). The `Open()` in `drivers/kafka.go` doesn't call `SecretFrom`.

**New behavior:** When SASL is enabled via settings, Kafka reads `SecretsFrom(ctx)` and configures a custom Dialer with SASL credentials.

**Implementation:**

```go
func (kafkaConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
    // ... existing broker/topic parsing ...

    // SASL authentication support
    saslMechanism, _ := stringSetting(def.Settings, "saslMechanism") // "PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512"
    
    var dialer *kafka.Dialer
    if saslMechanism != "" {
        secrets, _ := SecretsFrom(ctx)
        var username, password string
        if sec, ok := secrets["username"]; ok && !sec.IsZero() {
            username = string(sec.Reveal())
        }
        if sec, ok := secrets["password"]; ok && !sec.IsZero() {
            password = string(sec.Reveal())
        }
        
        var mechanism sasl.Mechanism
        var mechanismErr error
        switch saslMechanism {
        case "PLAIN":
            mechanism = plain.Mechanism{Username: username, Password: password}
        case "SCRAM-SHA-256":
            mechanism, mechanismErr = scram.Mechanism(scram.SHA256, username, password)
            if mechanismErr != nil {
                return nil, connect.NewConnError(connect.Validation, def.Key, "",
                    "SCRAM-SHA-256 mechanism error: "+mechanismErr.Error(), mechanismErr)
            }
        case "SCRAM-SHA-512":
            mechanism, mechanismErr = scram.Mechanism(scram.SHA512, username, password)
            if mechanismErr != nil {
                return nil, connect.NewConnError(connect.Validation, def.Key, "",
                    "SCRAM-SHA-512 mechanism error: "+mechanismErr.Error(), mechanismErr)
            }
        default:
            return nil, connect.NewConnError(connect.Validation, def.Key, "", 
                "unsupported SASL mechanism: "+saslMechanism, nil)
        }
        
        dialer = &kafka.Dialer{
            SASLMechanism: mechanism,
            Timeout:       10 * time.Second,
        }
    }

    return &kafkaClient{
        key:        def.Key,
        brokers:    brokers,
        topic:      topic,
        groupID:    groupID,
        partition:  partition,
        dlqTopic:   dlqTopic,
        maxRetries: maxRetries,
        dialer:     dialer,  // NEW field
    }, nil
}
```

### 2.6 RabbitMQ Connector — Discrete Credential Fields

**Current state:** RabbitMQ has a stub that reads but ignores the secret. The URL typically embeds credentials (`amqp://user:pass@host/vhost`).

**Decision: Use discrete settings for credentials** (recommended over URL parsing). This allows username to also be a secret and avoids complex URL manipulation.

**New settings:**
- `host`: string (required for new discrete-settings path)
- `port`: int (optional, default 5672)
- `vhost`: string (optional, default "/")
- `user`: string (optional, can be from settings if not secret)
- `url`: string (legacy, still supported)

**Backward compatibility for URL codepath:**
- Existing RabbitMQ connections using `url` setting continue to work exactly as before
- The `url` codepath does **not** support multi-secret injection (credentials are embedded in the URL)
- If `secretRefs` is provided alongside a `url` setting, the secrets are ignored (the URL is authoritative)
- Multi-secret support requires using the new discrete settings (`host`, `port`, `vhost`)
- No warning is logged in this case — the URL path is simply self-contained

**Implementation:**

```go
func (rabbitmqConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
    var url string
    
    // Try discrete settings first, fall back to URL
    if host, ok := stringSetting(def.Settings, "host"); ok && host != "" {
        port := 5672
        if p, ok := intSetting(def.Settings, "port"); ok {
            port = p
        }
        vhost := "/"
        if v, ok := stringSetting(def.Settings, "vhost"); ok && v != "" {
            vhost = v
        }
        
        // Get user/password from settings or secrets
        user, _ := stringSetting(def.Settings, "user")
        password := ""
        
        if secrets, ok := connect.SecretsFrom(ctx); ok {
            if sec, exists := secrets["username"]; exists && !sec.IsZero() {
                user = string(sec.Reveal())
            }
            if sec, exists := secrets["password"]; exists && !sec.IsZero() {
                password = string(sec.Reveal())
            }
        } else if sec, ok := connect.SecretFrom(ctx); ok && !sec.IsZero() {
            // Legacy: single secret is password
            password = string(sec.Reveal())
        }
        
        // Build URL from components
        if user != "" {
            url = fmt.Sprintf("amqp://%s:%s@%s:%d%s", 
                urlEscapeUser(user), urlEscapeUser(password), host, port, vhost)
        } else {
            url = fmt.Sprintf("amqp://%s:%d%s", host, port, vhost)
        }
    } else {
        // Legacy: use URL directly (no secret injection — URL is self-contained)
        url, _ = stringSetting(def.Settings, "url")
        if url == "" {
            return nil, connect.NewConnError(connect.Validation, def.Key, "", 
                "rabbitmq settings need url or host", nil)
        }
    }

    // ... rest of Open unchanged ...
}
```

---

## 3. Registry Resolution

### 3.1 Eager Registry (registry.go)

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/connect/registry.go`

Update the `open()` method (unexported, lines ~100-115) to resolve multiple secrets:

```go
// open resolves the connector + secret(s) for a def and builds the wrapped client.
func (r *registry) open(ctx context.Context, def ConnectionDef) (*resilientClient, error) {
    conn, ok := r.byType[def.Type]
    if !ok {
        return nil, newErr(Validation, def.Key, "", "no connector registered for type "+def.Type)
    }

    // Resolve secrets: prefer SecretRefs map over legacy SecretRef
    if len(def.SecretRefs) > 0 {
        secrets := make(map[string]Secret, len(def.SecretRefs))
        for name, ref := range def.SecretRefs {
            sec, err := r.secrets.Resolve(ctx, ref)
            if err != nil {
                return nil, wrapErr(Validation, def.Key, "", 
                    "resolve secret ref '"+name+"'", err)
            }
            secrets[name] = sec
        }
        ctx = WithSecrets(ctx, secrets)
        // Backward compat: also set single secret if "password" exists
        if pwdSec, ok := secrets["password"]; ok {
            ctx = WithSecret(ctx, pwdSec)
        }
    } else if def.SecretRef != "" {
        // Legacy single secret: treat as {"password": ref}
        sec, err := r.secrets.Resolve(ctx, def.SecretRef)
        if err != nil {
            return nil, wrapErr(Validation, def.Key, "", "resolve secret ref", err)
        }
        ctx = WithSecret(ctx, sec)
        ctx = WithSecrets(ctx, map[string]Secret{"password": sec})
    }

    inner, err := conn.Open(ctx, def)
    if err != nil {
        return nil, wrapErr(classOf(err), def.Key, "", "open connection", err)
    }
    return newResilientClient(def.Key, inner, def.Resilience), nil
}
```

**Precedence rule:** If both `SecretRefs` and `SecretRef` are set, `SecretRefs` wins. This allows gradual migration — existing connections with `SecretRef` continue working; new connections use `SecretRefs`.

### 3.2 Lazy Pool (pool.go)

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/connect/pool.go`

The pool's `open()` function is an **unexported method on `*ConnectionPool`** (lines ~195-212). The `registryV2` itself does not override `open()` — it delegates to `ConnectionPool.Get()` which internally calls `(*ConnectionPool).open()`.

Apply identical multi-secret resolution logic in `(*ConnectionPool).open()`:

```go
// open creates a new client for the given def.
func (p *ConnectionPool) open(ctx context.Context, def ConnectionDef) (Client, error) {
    conn, ok := p.byType[def.Type]
    if !ok {
        return nil, newErr(Validation, def.Key, "", "no connector registered for type "+def.Type)
    }

    // Resolve secrets: prefer SecretRefs map over legacy SecretRef
    if len(def.SecretRefs) > 0 {
        secrets := make(map[string]Secret, len(def.SecretRefs))
        for name, ref := range def.SecretRefs {
            sec, err := p.secrets.Resolve(ctx, ref)
            if err != nil {
                return nil, wrapErr(Validation, def.Key, "", 
                    "resolve secret ref '"+name+"'", err)
            }
            secrets[name] = sec
        }
        ctx = WithSecrets(ctx, secrets)
        if pwdSec, ok := secrets["password"]; ok {
            ctx = WithSecret(ctx, pwdSec)
        }
    } else if def.SecretRef != "" {
        sec, err := p.secrets.Resolve(ctx, def.SecretRef)
        if err != nil {
            return nil, wrapErr(Validation, def.Key, "", "resolve secret ref", err)
        }
        ctx = WithSecret(ctx, sec)
        ctx = WithSecrets(ctx, map[string]Secret{"password": sec})
    }

    inner, err := conn.Open(ctx, def)
    if err != nil {
        return nil, wrapErr(classOf(err), def.Key, "", "open connection", err)
    }
    p.openCount.Add(1)
    return newResilientClient(def.Key, inner, def.Resilience), nil
}
```

---

## 4. Schema Endpoints

### 4.1 Connector Registry Interface Extension

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/connect/connector.go`

The current `ConnectorLookup` interface only has `Connector(typ string) (Connector, bool)` — single lookup, no iteration. The schema endpoint needs to iterate all registered connector types. Add:

```go
// ConnectorRegistry extends ConnectorLookup with iteration capability.
// It is implemented by both registry and registryV2.
type ConnectorRegistry interface {
    ConnectorLookup
    // AllConnectors returns all registered connector factories keyed by type.
    AllConnectors() map[string]Connector
}
```

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/connect/registry.go`

Implement on eager registry:

```go
// AllConnectors returns all registered connector factories keyed by type.
func (r *registry) AllConnectors() map[string]Connector {
    r.mu.RLock()
    defer r.mu.RUnlock()
    result := make(map[string]Connector, len(r.byType))
    for k, v := range r.byType {
        result[k] = v
    }
    return result
}

// Compile-time assertion: registry implements ConnectorRegistry
var _ ConnectorRegistry = (*registry)(nil)
```

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/connect/registry_v2.go`

Implement on lazy registry:

```go
// AllConnectors returns all registered connector factories keyed by type.
func (r *registryV2) AllConnectors() map[string]Connector {
    r.mu.RLock()
    defer r.mu.RUnlock()
    result := make(map[string]Connector, len(r.byType))
    for k, v := range r.byType {
        result[k] = v
    }
    return result
}

// Compile-time assertion: registryV2 implements ConnectorRegistry
var _ ConnectorRegistry = (*registryV2)(nil)
```

### 4.2 GET /admin/connectors/schema

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/httpapi/admin_handlers.go`

Add new endpoint returning all connector schemas:

```go
// connectorSchemaResponse is the wire shape for connector schemas.
type connectorSchemaResponse struct {
    Connectors map[string]connectorSchema `json:"connectors"`
}

type connectorSchema struct {
    Secrets []connect.SecretField `json:"secrets"` // nil means dynamic
}

func (a *Admin) listConnectorSchemas(w http.ResponseWriter, r *http.Request) {
    registry, ok := a.deps.Conns.(connect.ConnectorRegistry)
    if !ok || a.deps.Conns == nil {
        writeError(w, http.StatusInternalServerError, "connector registry unavailable")
        return
    }

    connectors := registry.AllConnectors()
    
    schemas := make(map[string]connectorSchema, len(connectors))
    for typ, conn := range connectors {
        var secrets []connect.SecretField
        if sp, ok := conn.(connect.SecretSchemaProvider); ok {
            secrets = sp.SecretSchema() // may be nil for dynamic
        }
        schemas[typ] = connectorSchema{Secrets: secrets}
    }

    writeJSON(w, http.StatusOK, connectorSchemaResponse{Connectors: schemas})
}
```

**Response example:**
```json
{
  "connectors": {
    "postgres": { "secrets": [{"name": "password", "required": false, "label": "Database Password"}] },
    "kafka": { "secrets": [
      {"name": "password", "required": false, "label": "SASL Password"},
      {"name": "username", "required": false, "label": "SASL Username"}
    ]},
    "rest": { "secrets": null },
    "http": { "secrets": null }
  }
}
```

### 4.3 GET /admin/connectors/:type/schema (Optional)

For convenience, a single-connector variant:

```go
func (a *Admin) getConnectorSchema(w http.ResponseWriter, r *http.Request) {
    connType := r.PathValue("type")
    
    registry, ok := a.deps.Conns.(connect.ConnectorRegistry)
    if !ok {
        writeError(w, http.StatusInternalServerError, "connector registry unavailable")
        return
    }
    
    conn, found := registry.Connector(connType)
    if !found {
        writeError(w, http.StatusNotFound, "unknown connector type: "+connType)
        return
    }
    
    var secrets []connect.SecretField
    if sp, ok := conn.(connect.SecretSchemaProvider); ok {
        secrets = sp.SecretSchema()
    }
    
    writeJSON(w, http.StatusOK, connectorSchema{Secrets: secrets})
}
```

### 4.4 Route Registration

Add to `Admin.Mount()`:
```go
mux.HandleFunc("GET /admin/connectors/schema", a.listConnectorSchemas)
mux.HandleFunc("GET /admin/connectors/{type}/schema", a.getConnectorSchema)
```

---

## 5. Test Connection Endpoint

### 5.1 Request Shape Update

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/httpapi/admin_handlers.go`

Update `testConnectionRequest`:

```go
type testConnectionRequest struct {
    Type     string            `json:"type"`
    Settings map[string]any    `json:"settings,omitempty"`
    Secret   string            `json:"secret,omitempty"`   // Legacy single secret
    Secrets  map[string]string `json:"secrets,omitempty"`  // NEW: multiple secrets
}
```

### 5.2 Handler Update

**Important:** The request `secrets` field is `map[string]string` (plaintext values from the client), but `WithSecrets` requires `map[string]connect.Secret`. The handler must explicitly convert each string value using `connect.NewPlainSecret(value)` before calling `WithSecrets`.

```go
func (a *Admin) testConnection(w http.ResponseWriter, r *http.Request) {
    var req testConnectionRequest
    if err := decodeJSON(r, &req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid request body")
        return
    }
    if req.Type == "" {
        writeError(w, http.StatusBadRequest, "invalid request: type is required")
        return
    }

    lookup, ok := a.deps.Conns.(connect.ConnectorLookup)
    if !ok || a.deps.Conns == nil {
        writeError(w, http.StatusInternalServerError, "connector registry unavailable")
        return
    }
    connector, found := lookup.Connector(req.Type)
    if !found {
        writeError(w, http.StatusBadRequest, "unknown connection type: "+req.Type)
        return
    }

    def := connect.ConnectionDef{
        Key:      "_test",
        Type:     req.Type,
        Settings: req.Settings,
    }

    ctx := r.Context()

    // Build secrets: convert map[string]string → map[string]Secret
    // Prefer Secrets map over legacy Secret string
    if len(req.Secrets) > 0 {
        // Convert plaintext strings to Secret type
        secrets := make(map[string]connect.Secret, len(req.Secrets))
        for name, value := range req.Secrets {
            secrets[name] = connect.NewPlainSecret(value)
        }
        ctx = connect.WithSecrets(ctx, secrets)
        // Backward compat: also set single secret if "password" exists
        if pwdSec, ok := secrets["password"]; ok {
            ctx = connect.WithSecret(ctx, pwdSec)
        }
    } else if req.Secret != "" {
        // Legacy: treat as {"password": secret}
        sec := connect.NewPlainSecret(req.Secret)
        ctx = connect.WithSecret(ctx, sec)
        ctx = connect.WithSecrets(ctx, map[string]connect.Secret{"password": sec})
    }

    client, err := connector.Open(ctx, def)
    if err != nil {
        writeJSON(w, http.StatusOK, testConnectionResponse{
            Success: false,
            Error:   err.Error(),
        })
        return
    }
    defer client.Close()

    _, err = client.Execute(ctx, connect.Operation{Kind: "ping"})
    if err != nil {
        writeJSON(w, http.StatusOK, testConnectionResponse{
            Success: false,
            Error:   err.Error(),
        })
        return
    }

    writeJSON(w, http.StatusOK, testConnectionResponse{
        Success: true,
        Message: "Connection successful",
    })
}
```

### 5.3 Security Considerations

The test endpoint accepts plaintext secrets in the request body. This is intentional for testing but requires care:

- The test endpoint **MUST NOT** log request bodies (contains plaintext secrets)
- Consider rate limiting (e.g., 10 tests/minute per IP) to prevent credential stuffing — deferred to later hardening pass
- Test operations are NOT audited (no sensitive data enters the audit trail)
- The `observ.Redactor` should NOT process test payloads — they're ephemeral and never reach storage

---

## 6. CMS Schema Changes

### 6.1 Connection Content-Type

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/cms/src/api/connection/content-types/connection/schema.json`

Add `secretRefs` field:

```json
{
  "attributes": {
    "secretRef": {
      "type": "string",
      "maxLength": 256
    },
    "secretRefs": {
      "type": "json"
    }
  }
}
```

### 6.2 Validation Lifecycle Hook

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/cms/src/api/connection/content-types/connection/lifecycles.ts`

The file already exists with secret-denylist validation. The project uses Strapi with TypeScript (verified: existing `lifecycles.ts` uses `export default` pattern). Add `secretRefs` validation to the existing lifecycle:

```typescript
// Add to existing lifecycles.ts file

/**
 * Validates that secretRefs is a well-formed object with string values.
 * Accepts: null, undefined, or {string: string} map.
 * Rejects: arrays, non-string values, nested objects.
 */
function validateSecretRefs(refs: unknown): void {
  if (refs == null) return; // null/undefined is valid (no secrets)
  if (typeof refs !== 'object' || Array.isArray(refs)) {
    throw new errors.ValidationError('secretRefs must be an object');
  }
  for (const [k, v] of Object.entries(refs as Record<string, unknown>)) {
    if (typeof v !== 'string') {
      throw new errors.ValidationError(
        `secretRefs["${k}"] must be a string, got ${typeof v}`
      );
    }
  }
}

// Update existing beforeCreate/beforeUpdate hooks to include secretRefs validation:
export default {
  beforeCreate(event: { params: { data: { settings?: unknown; secretRefs?: unknown } } }) {
    assertNoSecretInSettings(event.params.data);
    validateSecretRefs(event.params.data.secretRefs);
  },
  beforeUpdate(event: { params: { data: { settings?: unknown; secretRefs?: unknown } } }) {
    assertNoSecretInSettings(event.params.data);
    validateSecretRefs(event.params.data.secretRefs);
  },
};
```

### 6.3 CMS Component Choice

| Approach | Pros | Cons |
|----------|------|------|
| **JSON field** | Simple, flexible, no new components needed | No schema validation in Strapi, free-form editing |
| **Repeatable component** | Typed key-value, better UX in default Strapi UI | More schema complexity, requires custom component definition |

**Decision: JSON field** for Phase 1. Rationale:
1. The plugin's custom UI will render a proper key-value editor anyway — Strapi's default JSON editor is rarely used directly
2. Simpler schema migration
3. REST needs arbitrary keys that a repeatable component with fixed fields couldn't express well
4. Validation lifecycle hook catches malformed data

If more structured validation is needed later, a repeatable component can be added without breaking existing data (JSON parses to the same shape).

### 6.4 Sync to Engine

When syncing connections to the engine, the sync logic builds `ConnectionDef`:

```typescript
// In CMS sync service
function toConnectionDef(conn: StrapiConnection): ConnectionDef {
  return {
    key: conn.key,
    type: conn.type,
    settings: flattenSettings(conn.settings),
    secretRef: conn.secretRef || '',
    secretRefs: conn.secretRefs || {},  // NEW: pass through as-is
    resilience: conn.resilience || {}
  };
}
```

---

## 7. CMS Admin UI Changes

### 7.1 Connections Page — Secret Refs Editor

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/cms/src/plugins/rule-engine/admin/src/pages/ConnectionsPage.tsx`

The connections table already shows `secretRef`. Update to show both:
- `secretRef` (legacy) if set and `secretRefs` is empty
- `secretRefs` key count if set (e.g., "3 secrets")

### 7.2 Connection Edit Form — SecretRefEditor Component

In the connection edit view, render a dynamic secret refs component:

1. **Fetch connector schema** on mount via `GET /rule-engine/connectors/schema`
2. **If schema is non-null (fixed schema connectors):** Render labeled fields for each `SecretField`
3. **If schema is null (REST/dynamic):** Render a repeatable key-value pair editor

**REST/Dynamic Secret UI Flow:**

1. The SecretRefEditor for REST renders a repeatable key-value pair component
2. "Key" field = HTTP header name (e.g., "Authorization")
3. "Value" field = secret ref string (e.g., "env:API_TOKEN")
4. User can add/remove rows dynamically (min 0, max unbounded)
5. When testing, the TestConnectionModal reads the keys from the form's current `secretRefs` state (not persisted yet) and renders a password input for each

```tsx
interface SecretRefEditorProps {
  connectorType: string;
  schema: SecretField[] | null; // null = dynamic
  value: Record<string, string>;
  onChange: (refs: Record<string, string>) => void;
}

function SecretRefEditor({ connectorType, schema, value, onChange }: SecretRefEditorProps) {
  if (schema === null) {
    // Dynamic: render key-value repeater for REST
    return (
      <KeyValueRepeater 
        value={value} 
        onChange={onChange}
        keyLabel="Header Name"
        valueLabel="Secret Ref (e.g., env:API_TOKEN)"
        addLabel="Add Secret Header"
      />
    );
  }

  // Fixed schema: render labeled fields
  return (
    <>
      {schema.map(field => (
        <Field.Root key={field.name}>
          <Field.Label>{field.label}{field.required && ' *'}</Field.Label>
          <TextInput
            placeholder={`env:${field.name.toUpperCase()}`}
            value={value[field.name] || ''}
            onChange={e => onChange({...value, [field.name]: e.target.value})}
          />
          <Field.Hint>Secret reference for {field.label.toLowerCase()}</Field.Hint>
        </Field.Root>
      ))}
    </>
  );
}
```

### 7.3 KeyValueRepeater Component

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/cms/src/plugins/rule-engine/admin/src/components/KeyValueRepeater/index.tsx`

New component for REST's dynamic secret refs:

```tsx
interface KeyValueRepeaterProps {
  value: Record<string, string>;
  onChange: (value: Record<string, string>) => void;
  keyLabel?: string;
  valueLabel?: string;
  addLabel?: string;
}

function KeyValueRepeater({ 
  value, 
  onChange, 
  keyLabel = "Key", 
  valueLabel = "Value",
  addLabel = "Add Row"
}: KeyValueRepeaterProps) {
  const entries = Object.entries(value);
  
  const handleKeyChange = (oldKey: string, newKey: string) => {
    const newValue = { ...value };
    const val = newValue[oldKey];
    delete newValue[oldKey];
    newValue[newKey] = val;
    onChange(newValue);
  };
  
  const handleValueChange = (key: string, val: string) => {
    onChange({ ...value, [key]: val });
  };
  
  const handleAdd = () => {
    // Generate unique key
    let i = 1;
    while (value[`Header-${i}`]) i++;
    onChange({ ...value, [`Header-${i}`]: '' });
  };
  
  const handleRemove = (key: string) => {
    const newValue = { ...value };
    delete newValue[key];
    onChange(newValue);
  };
  
  return (
    <Box>
      {entries.map(([key, val]) => (
        <Flex key={key} gap={2} marginBottom={2}>
          <TextInput
            placeholder={keyLabel}
            value={key}
            onChange={e => handleKeyChange(key, e.target.value)}
          />
          <TextInput
            placeholder={valueLabel}
            value={val}
            onChange={e => handleValueChange(key, e.target.value)}
          />
          <IconButton 
            label="Remove" 
            icon={<Trash />} 
            onClick={() => handleRemove(key)} 
          />
        </Flex>
      ))}
      <Button variant="secondary" onClick={handleAdd}>{addLabel}</Button>
    </Box>
  );
}
```

### 7.4 TestConnectionModal Update

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/cms/src/plugins/rule-engine/admin/src/components/TestConnectionModal/index.tsx`

Update to show multiple secret input fields:

1. Fetch connector schema on mount
2. If fixed schema: render input for each `SecretField`
3. If dynamic (REST): render inputs for each key in the connection's `secretRefs` **from the form state** (not persisted)
4. Send `secrets` map instead of single `secret`

```tsx
export const TestConnectionModal: React.FC<TestConnectionModalProps> = ({
  isOpen,
  onClose,
  connectionType,
  settings,
  secretRefs,  // NEW prop: current secretRefs from form (may not be persisted)
  connectionKey,
}) => {
  const { post, get } = useFetchClient();
  const [schema, setSchema] = React.useState<SecretField[] | null>(null);
  const [secrets, setSecrets] = React.useState<Record<string, string>>({});
  const [loading, setLoading] = React.useState(false);
  const [result, setResult] = React.useState<TestResult | null>(null);

  // Fetch schema on mount
  React.useEffect(() => {
    if (isOpen && connectionType) {
      get('/rule-engine/connectors/schema').then(res => {
        const connSchema = res.data?.connectors?.[connectionType]?.secrets ?? null;
        setSchema(connSchema);
        
        // Initialize secrets state
        if (connSchema) {
          // Fixed schema: initialize empty for each field
          const initial: Record<string, string> = {};
          connSchema.forEach(f => { initial[f.name] = ''; });
          setSecrets(initial);
        } else {
          // Dynamic (REST): initialize from secretRefs keys in form
          // User has defined which headers need secrets; we need values for each
          const initial: Record<string, string> = {};
          Object.keys(secretRefs || {}).forEach(k => { initial[k] = ''; });
          setSecrets(initial);
        }
      });
    }
  }, [isOpen, connectionType, secretRefs, get]);

  const handleTest = async () => {
    setLoading(true);
    setResult(null);
    
    try {
      const response = await post('/rule-engine/connections/test', {
        type: connectionType,
        settings: flattenSettings(settings),
        secrets: secrets,  // Send map instead of single secret
      });
      setResult(response.data);
    } catch (err) {
      setResult({ success: false, error: err.message });
    } finally {
      setLoading(false);
    }
  };

  // Render secret inputs based on schema or secretRefs keys
  const renderSecretInputs = () => {
    if (schema) {
      // Fixed schema: labeled inputs
      return schema.map(field => (
        <Field.Root key={field.name}>
          <Field.Label>{field.label}{field.required && ' *'}</Field.Label>
          <TextInput
            type="password"
            placeholder={`Enter ${field.label.toLowerCase()}`}
            value={secrets[field.name] || ''}
            onChange={e => setSecrets(s => ({ ...s, [field.name]: e.target.value }))}
          />
        </Field.Root>
      ));
    }
    
    // Dynamic (REST): inputs for each key defined in secretRefs
    const keys = Object.keys(secretRefs || {});
    if (keys.length === 0) {
      return <Typography variant="pi">No secret headers defined for this connection.</Typography>;
    }
    
    return keys.map(key => (
      <Field.Root key={key}>
        <Field.Label>{key} (Header Value)</Field.Label>
        <TextInput
          type="password"
          placeholder={`Enter value for ${key} header`}
          value={secrets[key] || ''}
          onChange={e => setSecrets(s => ({ ...s, [key]: e.target.value }))}
        />
        <Field.Hint>Secret ref in connection: {secretRefs[key]}</Field.Hint>
      </Field.Root>
    ));
  };

  return (
    <Modal isOpen={isOpen} onClose={onClose}>
      <Modal.Header>Test Connection: {connectionKey}</Modal.Header>
      <Modal.Body>
        {renderSecretInputs()}
        {result && (
          <Box marginTop={4}>
            {result.success ? (
              <Typography textColor="success600">✓ {result.message}</Typography>
            ) : (
              <Typography textColor="danger600">✗ {result.error}</Typography>
            )}
          </Box>
        )}
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onClick={onClose}>Cancel</Button>
        <Button onClick={handleTest} loading={loading}>Test Connection</Button>
      </Modal.Footer>
    </Modal>
  );
};
```

---

## 8. Admin API — createConnectionRequest Update

**File:** `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs/engine/internal/httpapi/admin_handlers.go`

The existing `createConnectionRequest` struct (line ~170) has only `SecretRef string`. Add multi-secret support:

```go
type createConnectionRequest struct {
    Env        string                    `json:"env,omitempty"`
    Key        string                    `json:"key"`
    Type       string                    `json:"type"`
    Settings   map[string]any            `json:"settings,omitempty"`
    SecretRef  string                    `json:"secretRef,omitempty"`   // Legacy
    SecretRefs map[string]string         `json:"secretRefs,omitempty"`  // NEW: multi-secret refs
    Resilience *connect.ResiliencePolicy `json:"resilience,omitempty"`
    // ... existing reject-if-present fields for password, secret, token, apiKey ...
}
```

Update `createConnection` handler to populate `def.SecretRefs`:

```go
func (a *Admin) createConnection(w http.ResponseWriter, r *http.Request) {
    // ... existing validation ...

    def := connect.ConnectionDef{
        Key:        req.Key,
        Type:       req.Type,
        Settings:   req.Settings,
        SecretRef:  req.SecretRef,
        SecretRefs: req.SecretRefs,  // NEW
    }
    if req.Resilience != nil {
        def.Resilience = *req.Resilience
    }

    // ... rest of handler ...
}
```

---

## 9. Error Handling

### 9.1 Secret Resolution Failures

When resolving multiple secrets, fail fast on the first resolution error:

```go
for name, ref := range def.SecretRefs {
    sec, err := r.secrets.Resolve(ctx, ref)
    if err != nil {
        return nil, wrapErr(Validation, def.Key, "", 
            "resolve secret ref '"+name+"'", err)
    }
    secrets[name] = sec
}
```

The error message includes the secret name (e.g., "resolve secret ref 'username'") so operators know which env var is missing. The ref value itself is safe to include in logs (it's "env:VAR_NAME", not the secret).

### 9.2 Missing Required Secrets

The `SecretField.Required` flag is informational for the UI. The engine does not enforce it at Open time — connectors already handle missing credentials gracefully (e.g., postgres without a password connects to a trust-auth DB). If strict enforcement is needed later, add validation in the registry before Open.

---

## 10. Testability

### 10.1 Unit Tests

| Component | Test Cases |
|-----------|------------|
| `WithSecrets`/`SecretsFrom` | Round-trip secrets map through context |
| Registry `open()` | Multi-secret resolution; legacy single-secret fallback; error on missing env var |
| Pool `open()` | Same as registry; singleflight behavior unchanged |
| Connector `SecretSchema()` | Each connector returns expected schema |
| Connector `Open()` | Opens with secrets map; falls back to single secret; handles empty secrets |
| Test endpoint | Accepts `secrets` map; backward compat with `secret` string |
| Schema endpoint | Returns all connectors with correct schemas; REST returns null |

### 10.2 Integration Tests

- Create connection with `secretRefs`, sync to engine, verify connector receives secrets
- Test endpoint with multi-secret payload succeeds
- Schema endpoint returns all connectors with correct schemas
- REST connection with secret headers receives resolved values
- Kafka SASL authentication works with username/password secrets

---

## 11. Migration and Backward Compatibility

### 11.1 Existing Connections

Connections with only `secretRef` (string) continue to work:
1. Registry detects `SecretRefs` is empty/nil
2. Falls back to `SecretRef`, resolves it
3. Passes as `{"password": secret}` to `WithSecrets`
4. Also calls `WithSecret` for connectors still using `SecretFrom`

### 11.2 Connector Code

Existing connector code using `SecretFrom(ctx)` continues to work because:
1. Registry always calls `WithSecret(ctx, secrets["password"])` when a "password" key exists
2. Connectors can gradually migrate to `SecretsFrom(ctx)` at their own pace

### 11.3 CMS Data

No migration needed:
- `secretRef` column remains as-is
- New `secretRefs` JSON column is nullable, defaults to null
- Existing connections display and function without modification

---

## 12. Files to Modify

### Engine

| File | Changes |
|------|---------|
| `engine/internal/connect/connect.go` | Add `SecretRefs map[string]string` to `ConnectionDef` |
| `engine/internal/connect/connector.go` | Add `SecretField`, `SecretSchemaProvider`, `ConnectorRegistry`, `WithSecrets`, `SecretsFrom` |
| `engine/internal/connect/registry.go` | Update `open()` for multi-secret resolution; add `AllConnectors()`; add compile-time assertion |
| `engine/internal/connect/pool.go` | Update `(*ConnectionPool).open()` for multi-secret resolution |
| `engine/internal/connect/registry_v2.go` | Add `AllConnectors()`; add compile-time assertion |
| `engine/internal/connect/drivers/postgres.go` | Add `SecretSchema()`, update `Open()` to prefer `SecretsFrom` |
| `engine/internal/connect/drivers/mysql.go` | Add `SecretSchema()`, update `Open()` to prefer `SecretsFrom` |
| `engine/internal/connect/drivers/valkey.go` | Add `SecretSchema()`, update `Open()` to prefer `SecretsFrom` |
| `engine/internal/connect/drivers/kafka.go` | Add `SecretSchema()`, add SASL auth support in `Open()`, add `dialer` field to client |
| `engine/internal/connect/drivers/rabbitmq.go` | Add `SecretSchema()`, implement discrete credential fields in `Open()` |
| `engine/internal/connect/drivers/rest.go` | Add `SecretSchema()` returning nil, add secret-as-header support in `Open()` |
| `engine/internal/httpapi/admin_handlers.go` | Add schema endpoints, update test endpoint, update createConnectionRequest |
| `engine/internal/httpapi/routes.go` | Register schema routes |

### CMS

| File | Changes |
|------|---------|
| `cms/src/api/connection/content-types/connection/schema.json` | Add `secretRefs` JSON field |
| `cms/src/api/connection/content-types/connection/lifecycles.ts` | Add `validateSecretRefs()` to existing hooks |
| `cms/src/plugins/rule-engine/admin/src/pages/ConnectionsPage.tsx` | Show secretRefs count |
| `cms/src/plugins/rule-engine/admin/src/components/TestConnectionModal/index.tsx` | Multi-secret inputs |
| `cms/src/plugins/rule-engine/admin/src/components/SecretRefEditor/index.tsx` | NEW: editor component |
| `cms/src/plugins/rule-engine/admin/src/components/KeyValueRepeater/index.tsx` | NEW: repeater component |
| `cms/src/plugins/rule-engine/server/src/services/engine.ts` | Sync secretRefs to engine |

---

## 13. Summary of Ambiguous Decisions Resolved

1. **Connector interface change (exact signatures):**
   - `SecretSchemaProvider` is a separate optional interface (not a method on `Connector`) — avoids breaking existing connectors
   - `SecretField struct { Name string; Required bool; Label string }`
   - Type assertion at runtime: `if sp, ok := conn.(SecretSchemaProvider); ok { ... }`
   - All 6 connectors affected: postgres, mysql, valkey, kafka, rabbitmq, rest (plus http alias)

2. **REST dynamic secrets:**
   - `SecretSchema()` returns `nil` to signal dynamic
   - The `secretRefs` map keys are HTTP header names (e.g., `{"Authorization": "env:TOKEN"}`)
   - Registry resolves all refs; REST `Open()` applies them as default headers (overriding static headers from settings)
   - **Current state:** REST ignores secrets entirely — this is net-new behavior

3. **CMS component choice:**
   - **JSON field** for `secretRefs` (not repeatable component)
   - Lifecycle hook validates shape at save time
   - Custom UI handles the editing UX with `KeyValueRepeater` for REST

4. **Migration path:**
   - No DB migration required
   - `secretRefs` is a new nullable JSON column alongside existing `secretRef` string
   - Engine receives structured data from CMS sync (not raw DB)
   - Existing connections function without modification

5. **Context helper signatures:**
   - `WithSecrets(ctx context.Context, secrets map[string]Secret) context.Context`
   - `SecretsFrom(ctx context.Context) (map[string]Secret, bool)`
   - Legacy `SecretRef` maps to `{"password": ref}` via both `WithSecrets` and `WithSecret` for backward compat

---

## 14. Review Finding Responses

| ID | Severity | Finding | Resolution |
|----|----------|---------|------------|
| **H2** | HIGH | Kafka SCRAM mechanism error handling discarded | **Fixed:** Added explicit error handling for `scram.Mechanism()` calls in Section 2.5. Errors now return `NewConnError` with mechanism-specific message. |
| **H3** | HIGH | REST connector nil map initialization | **Fixed:** Section 2.4 now initializes `defaultHeaders` map *before* the secrets loop with `if defaultHeaders == nil { defaultHeaders = make(map[string]string) }`. |
| **M1** | MEDIUM | CMS lifecycle hook path unverified | **Verified:** Path is correct — `lifecycles.ts` already exists at `cms/src/api/connection/content-types/connection/lifecycles.ts` with `export default` pattern (Strapi v4 TypeScript). Updated Section 6.2 to show adding validation to existing file. |
| **M2** | MEDIUM | ConnectorRegistry interface type assertion will fail | **Fixed:** Added compile-time assertions in Section 4.1: `var _ ConnectorRegistry = (*registry)(nil)` and `var _ ConnectorRegistry = (*registryV2)(nil)`. |
| **M3** | MEDIUM | RabbitMQ URL codepath backward compat unclear | **Clarified:** Section 2.6 now explicitly states: URL path remains unchanged, secrets are NOT injected into URL path, multi-secret requires discrete settings (host/port/vhost). |
| **M4** | MEDIUM | Test endpoint type conversion not emphasized | **Fixed:** Section 5.2 now has explicit note: "The request `secrets` field is `map[string]string` (plaintext values from the client), but `WithSecrets` requires `map[string]connect.Secret`." Code comments reinforced. |
| **M5** | MEDIUM | createConnectionRequest missing SecretRefs field | **Fixed:** Added Section 8 covering `createConnectionRequest` struct update and handler changes. |
| **M6** | MEDIUM | Frozen contract modification not acknowledged | **Fixed:** Section 1.1 now includes explicit "Frozen contract acknowledgment" paragraph explaining this is an additive, non-breaking change. |
