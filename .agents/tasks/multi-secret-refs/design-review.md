# TASK-009: Multi-Secret References — Design Review

**Reviewer:** Design Review Agent  
**Design Version:** design.md (with Review Finding Responses in §14)  
**Requirements Version:** requirements.md

---

## Executive Summary

The design is thorough and addresses the core requirements well. The design author has already incorporated fixes for several issues (documented in §14). However, **one HIGH finding** remains unaddressed, and there are additional MEDIUM findings discovered during source verification.

**Verdict:** CHANGES_REQUESTED (1 HIGH, 3 MEDIUM)

---

## Findings

### HIGH Findings

#### H1: registryV2 Does NOT Have Its Own `open()` Method — Design Describes Non-Existent Code Path

**Location:** Design §3.2 "Lazy Pool (pool.go)"

**Problem:** The design states:
> "The `registryV2` itself does not override `open()` — it delegates to `ConnectionPool.Get()` which internally calls `(*ConnectionPool).open()`."

This is correct, and the design says to apply multi-secret logic to `(*ConnectionPool).open()` in `pool.go`. However, the design **also** says in §4.1:
> "Implement on lazy registry... `var _ ConnectorRegistry = (*registryV2)(nil)`"

And the Files to Modify table in §12 lists:
> "`engine/internal/connect/registry_v2.go` — Add `AllConnectors()`; add compile-time assertion"

**Verified reality:** Looking at `registry_v2.go`, the `registryV2` struct delegates to `ConnectionPool` for client retrieval, but `ConnectionPool.open()` in `pool.go` (lines ~205-222) currently only handles single `SecretRef`. The design correctly identifies that `pool.go`'s `open()` method needs the multi-secret logic.

**However**, the design's proposed `ConnectionPool.open()` code snippet (§3.2) references `p.secrets.Resolve()` but the actual pool stores secrets in a field named `secrets` (verified at line 65 of pool.go: `secrets SecretProvider`). This is correct.

**The actual HIGH issue:** The design does NOT show updating the `ConnectionPool` struct definition to be aware it now needs to call `WithSecrets` in addition to `WithSecret`. The snippet is correct, but the design never addresses that `ConnectionPool.open()` must import/call the new `WithSecrets` function which will be defined in `connector.go`. This is an implementation detail, but the design should explicitly mention that `pool.go` will need to call the new helpers.

**More critically:** The design's code snippet in §3.2 shows `p.byType` but the actual `ConnectionPool` struct field is named `byType` (verified). However, the design shows the pool calling `conn.Open(ctx, def)` directly. The pool DOES do this (line ~212). **The gap is**: the design's code never shows how `resilientClient` is created. Looking at the actual `pool.go` line 218: `return newResilientClient(def.Key, inner, def.Resilience), nil`. The design snippet omits this, which could mislead the implementer.

**Severity:** HIGH — The design's pool.open() snippet is incomplete; it shows secret resolution but not the actual `newResilientClient` wrapping that follows. An implementer copy-pasting would produce broken code.

**Fix:** Update §3.2's `(*ConnectionPool).open()` snippet to include the full function body including the `newResilientClient` return and the `p.openCount.Add(1)` metric increment that the current code has:

```go
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
    p.openCount.Add(1)  // MISSING from design
    return newResilientClient(def.Key, inner, def.Resilience), nil  // MISSING from design
}
```

---

### MEDIUM Findings

#### M1: CMS Connection Content-Type Uses `dynamiczone` for Settings — secretRefs Needs Schema Coordination

**Location:** Design §6.1 "Connection Content-Type"

**Problem:** The design says to add `secretRefs` as a JSON field to `schema.json`. Looking at the actual CMS schema, `settings` is a `dynamiczone` with typed components per connector type:

```json
"settings": {
  "type": "dynamiczone",
  "components": [
    "connection.postgres-settings",
    "connection.rest-settings",
    ...
  ]
}
```

This means the UI already knows the connector type from which settings component is selected. The design's `secretRefs` JSON field will work, but the design doesn't address:

1. How the UI will know which `SecretField` schema to render when the settings dynamiczone component changes
2. Whether the existing settings components need to be updated (e.g., does `rest-settings` component need awareness of secret headers?)

**Fix:** Add a note in §6 clarifying that:
- The connector type is inferred from the settings dynamiczone component selection
- The `SecretRefEditor` component determines the schema by mapping the selected settings component name to connector type (e.g., `connection.rest-settings` → `rest`)
- No changes to existing settings components are required; `secretRefs` is orthogonal

#### M2: RabbitMQ Backward Compat for URL Path — Silent Secret Ignore May Confuse Users

**Location:** Design §2.6 "RabbitMQ Connector"

**Problem:** The design states:
> "If `secretRefs` is provided alongside a `url` setting, the secrets are ignored (the URL is authoritative)"
> "No warning is logged in this case — the URL path is simply self-contained"

This silent behavior could confuse users who set both `url` (with embedded credentials) and `secretRefs` expecting the secrets to override. The design explicitly says NOT to warn, but this seems user-hostile.

**Verified:** The requirements don't mandate a warning, but they do say (NFR-1): "No breaking changes to existing connections." Silent ignore is technically backward-compatible, but it could mask misconfiguration.

**Fix:** Change the design to log an info-level message when both `url` and `secretRefs` are provided:
```go
if urlSet && len(def.SecretRefs) > 0 && log != nil {
    log.Emit(ctx, "info", "rabbitmq: secretRefs ignored when url is set", 
        map[string]any{"key": def.Key})
}
```

Alternatively, document this behavior in the CMS UI by showing a hint when URL mode is selected.

#### M3: `testConnectionRequest` Missing Documentation of Secrets Type Conversion

**Location:** Design §5.2 "Handler Update"

**Problem:** The design correctly notes:
> "The request `secrets` field is `map[string]string` (plaintext values from the client), but `WithSecrets` requires `map[string]connect.Secret`."

And the code shows `connect.NewPlainSecret(value)`. However, looking at the existing `testConnectionRequest` struct in `admin_handlers.go`:

```go
type testConnectionRequest struct {
    Type     string         `json:"type"`
    Settings map[string]any `json:"settings,omitempty"`
    Secret   string         `json:"secret,omitempty"`
}
```

The design proposes adding `Secrets map[string]string`. The JSON wire format is correct, but the design doesn't show the updated struct definition inline in §5.1 — it only shows the struct shape. The implementer needs to ensure the field is named `Secrets` (capital S) with json tag `"secrets"`.

**Fix:** §5.1 shows the correct struct, but add an explicit note that the field must be `Secrets map[string]string \`json:"secrets,omitempty"\`` to match the JSON key "secrets" while following Go naming conventions.

---

### NIT Findings

#### N1: Kafka SecretSchema Doesn't Include Optional Certs for TLS

**Location:** Design §2.2 "Per-Connector Impact" table

**Problem:** The design shows Kafka's schema as:
```
[{Name: "password", Required: false, Label: "SASL Password"}, 
 {Name: "username", Required: false, Label: "SASL Username"}]
```

The requirements table shows Kafka needing: "username (SASL), sslCert, sslKey, sslCA". The design only declares username/password. While Out of Scope §4 says "mTLS certificate file handling... is connector-specific," the `SecretSchema` should at least declare the fields so the UI can render them even if the connector doesn't use them yet.

**Fix:** Add optional cert fields to Kafka's schema for future-proofing:
```go
[
  {Name: "password", Required: false, Label: "SASL Password"},
  {Name: "username", Required: false, Label: "SASL Username"},
  {Name: "sslCert", Required: false, Label: "TLS Certificate"},
  {Name: "sslKey", Required: false, Label: "TLS Private Key"},
  {Name: "sslCA", Required: false, Label: "TLS CA Certificate"},
]
```

#### N2: Valkey SecretSchema Could Include Username for ACL

**Location:** Design §2.2 table

**Problem:** Valkey schema shows only `password`, but the requirements table shows Valkey needs "username (ACL), tlsCert". Looking at the actual `valkey.go` driver (line 49-50), it already reads `user` from settings:
```go
if user, ok := stringSetting(def.Settings, "user"); ok {
    opt.Username = user
}
```

So username comes from settings, not secrets. This is a design decision (username in settings, password in secrets). The schema is correct, but the design should clarify this is intentional.

**Fix:** Add a note explaining that Valkey username is a non-secret setting while password is a secret. If ACL requires username to also be secret-protected, that's a future enhancement.

#### N3: Missing Import Statement for `scram` and `sasl` Packages in Kafka

**Location:** Design §2.5 "Kafka SASL Authentication Support"

**Problem:** The code snippet shows:
```go
mechanism, mechanismErr = scram.Mechanism(scram.SHA256, username, password)
```

But doesn't show the import for `github.com/segmentio/kafka-go/sasl/scram` and `github.com/segmentio/kafka-go/sasl/plain`.

**Fix:** Add imports to the code snippet for completeness, or note that the implementer must add them.

---

## Verified Assumptions

| Assumption | Verification | Status |
|------------|--------------|--------|
| `ConnectionDef` has `SecretRef string` field | Verified in `connect.go` line 86 | ✅ Correct |
| Registry's `open()` method is at lines ~100-115 | Verified at lines 97-115 in `registry.go` | ✅ Correct |
| Pool's `open()` is on `*ConnectionPool` | Verified at lines 205-222 in `pool.go` | ✅ Correct |
| `registryV2` has `Connector()` method | Verified at lines 139-144 in `registry_v2.go` | ✅ Correct |
| CMS `lifecycles.ts` exists and uses `export default` | Verified — file exists with correct pattern | ✅ Correct |
| REST driver does NOT call `SecretFrom` | Verified — `rest.go` has no `SecretFrom` call | ✅ Correct |
| Kafka driver does NOT use secrets | Verified — `kafka.go` has no `SecretFrom` call | ✅ Correct |
| RabbitMQ reads secret but ignores it | Verified at lines 57-60 of `rabbitmq.go` — reads but only assigns to `_ = sec` | ✅ Correct |
| Postgres/MySQL/Valkey use `SecretFrom(ctx)` | Verified in each driver's `Open()` | ✅ Correct |
| `Secret` has `NewPlainSecret` constructor | Verified in `secret.go` line 32 | ✅ Correct |
| `testConnectionRequest` exists in `admin_handlers.go` | Verified at ~line 308 | ✅ Correct |

## Unverified/Wrong Assumptions

| Claim | Reality | Impact |
|-------|---------|--------|
| None found | N/A | N/A |

---

## Summary

The design is comprehensive and the author has proactively addressed several issues in the §14 "Review Finding Responses" section. The remaining findings are:

- **1 HIGH**: Pool's `open()` snippet is incomplete (missing `newResilientClient` and metrics)
- **3 MEDIUM**: CMS dynamiczone coordination unclear, RabbitMQ silent ignore confusing, test request struct needs json tag clarification

All findings have concrete fixes. After incorporating these changes, the design should be ready for implementation.
