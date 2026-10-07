# TASK-009: Multi-Secret References for Connections

## Summary
Extend the connection model to support multiple named secrets per connection, replacing the single `secretRef` string with a `secretRefs` map. This enables connectors that need multiple credentials (Kafka SASL, REST with multiple auth headers, mTLS certs).

## Motivation
Current limitation: 1 connection = 1 secret (typically password). Real-world needs:

| Connector | Current | Needed |
|-----------|---------|--------|
| postgres | password ✓ | sslCert, sslKey (mTLS) |
| mysql | password ✓ | sslCert, sslKey |
| valkey | password ✓ | username (ACL), tlsCert |
| rest | ❌ limited | Any headers (Authorization, X-API-Key, custom) |
| kafka | password ✓ | username (SASL), sslCert, sslKey, sslCA |
| rabbitmq | password ✓ | username, clientCert, clientKey |

REST is urgent — it's fundamentally multi-secret and user-defined (users choose which headers need secrets).

## Design

### Data Model Change
```go
// Engine ConnectionDef
type ConnectionDef struct {
    Key        string
    Type       string
    Settings   map[string]any
    SecretRef  string             // legacy single secret (backward compat)
    SecretRefs map[string]string  // NEW: named secrets {"password": "env:X", "username": "env:Y"}
    Resilience ResiliencePolicy
}
```

### Database Storage (no schema change)
Store `secret_ref` as JSONB, interpret as:
- String `"env:PG_PASSWORD"` → legacy single secret
- Object `{"password": "env:X", "username": "env:Y"}` → multi-secret

### CMS UI
Dynamic form based on connector type:
```
Secret References:
  ┌──────────┬────────────────────┐
  │ Name     │ Secret Ref         │
  ├──────────┼────────────────────┤
  │ password │ env:KAFKA_PASSWORD │
  │ username │ env:KAFKA_USERNAME │
  └──────────┴────────────────────┘
  [+ Add Secret]
```

For REST (user-defined headers):
```
Secret Headers:
  ┌───────────────────┬─────────────────┐
  │ Header Name       │ Secret Ref      │
  ├───────────────────┼─────────────────┤
  │ Authorization     │ env:API_TOKEN   │
  │ X-API-Key         │ env:API_KEY     │
  └───────────────────┴─────────────────┘
  [+ Add Secret Header]
```

### Connector Interface Extension
```go
type Connector interface {
    Type() string
    Open(ctx context.Context, def ConnectionDef) (Client, error)
    
    // NEW: Declare what secrets this connector supports
    SecretSchema() []SecretField
}

type SecretField struct {
    Name     string  // "password", "username", "clientCert"
    Required bool
    Label    string  // "Database Password", "SASL Username"
}
```

- Fixed connectors (postgres, kafka) declare known fields
- REST returns nil (dynamic — user provides any header names)
- CMS queries engine for schema to render the right form

### Secret Resolution
```go
func (r *registry) open(ctx context.Context, def ConnectionDef) (*resilientClient, error) {
    secrets := make(map[string]Secret)
    
    // New multi-secret path
    if len(def.SecretRefs) > 0 {
        for name, ref := range def.SecretRefs {
            sec, err := r.secrets.Resolve(ctx, ref)
            if err != nil {
                return nil, err
            }
            secrets[name] = sec
        }
    } else if def.SecretRef != "" {
        // Legacy single secret → default to "password" key
        sec, err := r.secrets.Resolve(ctx, def.SecretRef)
        if err != nil {
            return nil, err
        }
        secrets["password"] = sec
    }
    
    ctx = WithSecrets(ctx, secrets)  // NEW: pass map instead of single
    return conn.Open(ctx, def)
}
```

### Test Connection Update
Extend test endpoint to accept multiple secrets:
```json
{
  "type": "kafka",
  "settings": { "brokers": "kafka:9092" },
  "secrets": {
    "username": "actual_user",
    "password": "actual_pass"
  }
}
```

## Implementation Steps

1. **Engine: Extend ConnectionDef** — add SecretRefs field
2. **Engine: Update registry** — resolve multiple secrets, pass via WithSecrets
3. **Engine: Update connectors** — read from secrets map
4. **Engine: SecretSchema interface** — connectors declare their fields
5. **Engine: GET /admin/connectors/schema** — endpoint to query connector schemas
6. **Engine: Update test endpoint** — accept secrets map
7. **CMS: Schema change** — secretRefs as JSON component
8. **CMS: UI** — dynamic secret refs form based on connector schema
9. **CMS: Test modal** — show fields from connector schema

## Backward Compatibility
- Existing `secretRef` (string) continues to work
- If only `secretRef` is set, treat as `{"password": "..."}` 
- No migration needed — same column, smarter parsing

## Priority
Medium — REST connector is limited without this. Other connectors work with single password but can't do mTLS/SASL.

## Dependencies
- TASK-010 (if created): Connector schema endpoint
