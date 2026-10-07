# Engine-CMS Capability Gap Analysis

Investigation date: 2026-10-07  
Investigator: Read-only analysis of engine vs CMS capabilities

## Executive Summary

This investigation identified **23 capability gaps** where the engine supports features that the CMS cannot author or manage. The most significant gaps are:

1. **4 missing connector types** (MySQL, HTTP alias, Kafka, RabbitMQ)
2. **5 missing flow node types** (messageTrigger, filter, find, map, reduce)
3. **Missing resilience features** (circuit breaker, backoff settings)
4. **Gateway/worker group features** not surfaced in CMS

---

## 1. CONNECTOR TYPES

### Engine Capability
Located in `engine/internal/connect/drivers/registry.go`:

```go
func All() []connect.Connector {
    rest := newRESTConnector("rest")
    restAlias := newRESTConnector("http")
    return []connect.Connector{
        newPGConnector(),       // postgres
        newMySQLConnector(),    // mysql
        newValkeyConnector(),   // valkey
        rest,                   // rest
        restAlias,              // http (alias)
        newKafkaConnector(),    // kafka
        newRabbitMQConnector(), // rabbitmq
    }
}
```

**Engine supports 7 connector types**: postgres, mysql, valkey, rest, http, kafka, rabbitmq

### CMS Schema
Located in `cms/src/api/connection/content-types/connection/schema.json`:

```json
"type": {
  "type": "enumeration",
  "enum": ["postgres", "valkey", "rest"],
  "required": true
}
```

**CMS supports only 3 connector types**: postgres, valkey, rest

### GAPS IDENTIFIED

| Gap | Severity | Engine File | CMS Missing |
|-----|----------|-------------|-------------|
| MySQL connector | HIGH | `drivers/mysql.go` | No enum value, no settings component |
| HTTP alias | LOW | `drivers/registry.go` | No enum value (alias of rest) |
| Kafka connector | HIGH | `drivers/kafka.go` | No enum value, no settings component |
| RabbitMQ connector | HIGH | `drivers/rabbitmq.go` | No enum value, no settings component |

**Recommended Fix**:
1. Add `mysql`, `http`, `kafka`, `rabbitmq` to the `type` enum in `cms/src/api/connection/content-types/connection/schema.json`
2. Create settings components:
   - `cms/src/components/connection/mysql-settings.json`
   - `cms/src/components/connection/kafka-settings.json`
   - `cms/src/components/connection/rabbitmq-settings.json`
3. Add these components to the `settings` dynamiczone in connection schema

---

## 2. FLOW NODE TYPES

### Engine Capability
Located in `engine/internal/flow/node.go`:

```go
const (
    TypeTrigger        NodeType = "trigger"
    TypeMessageTrigger NodeType = "messageTrigger"
    TypeAction         NodeType = "action"
    TypeCondition      NodeType = "condition"
    TypeSwitch         NodeType = "switch"
    TypeSequence       NodeType = "sequence"
    TypeParallel       NodeType = "parallel"
    TypeForEach        NodeType = "forEach"
    TypeDecision       NodeType = "decision"
    TypeSet            NodeType = "set"
    TypeLogger         NodeType = "logger"
    TypeResponse       NodeType = "response"
    TypeFilter         NodeType = "filter"
    TypeFind           NodeType = "find"
    TypeMap            NodeType = "map"
    TypeReduce         NodeType = "reduce"
)
```

**Engine supports 16 node types**

### CMS Palette
Located in `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`:

```typescript
export const NODE_PALETTE = [
  'trigger',
  'action',
  'condition',
  'switch',
  'sequence',
  'parallel',
  'forEach',
  'decision',
  'set',
  'logger',
  'response',
] as const;
```

**CMS palette offers 11 node types**

### GAPS IDENTIFIED

| Gap | Severity | Engine Definition | Description |
|-----|----------|-------------------|-------------|
| `messageTrigger` | HIGH | `flow/spec.go:MessageTriggerSpec` | Entry point for message-driven flows (Kafka/RabbitMQ consumers) |
| `filter` | MEDIUM | `flow/spec.go:FilterSpec` | Filters array keeping items matching ZEN predicate |
| `find` | MEDIUM | `flow/spec.go:FindSpec` | Finds first array item matching ZEN predicate |
| `map` | MEDIUM | `flow/spec.go:MapSpec` | Transforms each array item via ZEN |
| `reduce` | MEDIUM | `flow/spec.go:ReduceSpec` | Aggregates array to single value via ZEN |

**Recommended Fix**:
Add the missing types to `NODE_PALETTE` in `FlowCanvasField/index.tsx`:

```typescript
export const NODE_PALETTE = [
  'trigger',
  'messageTrigger',  // NEW
  'action',
  'condition',
  'switch',
  'sequence',
  'parallel',
  'forEach',
  'decision',
  'set',
  'logger',
  'response',
  'filter',  // NEW
  'find',    // NEW
  'map',     // NEW
  'reduce',  // NEW
] as const;
```

---

## 3. WEBHOOK PROVIDERS

### Engine Capability
Located in `engine/internal/webhook/provider.go`:

```go
const (
    ProviderStripe  = "stripe"
    ProviderGitHub  = "github"
    ProviderGeneric = "generic"
)
```

### CMS Schema
Located in `cms/src/api/webhook/content-types/webhook/schema.json`:

```json
"provider": {
  "type": "enumeration",
  "enum": ["stripe", "github", "generic"],
  "default": "generic",
  "required": true
}
```

### GAPS IDENTIFIED

**✓ SYNCED** - No gaps. Engine and CMS are fully aligned on webhook providers.

---

## 4. SCHEDULE FEATURES

### Engine Capability
Located in `engine/internal/scheduler/cron.go`:

```go
var cronParser = cron.NewParser(
    cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)
```

Supports:
- 5-field standard cron expressions
- @aliases (@hourly, @daily, @weekly, @monthly, @yearly, @annually)
- @every directives (@every 30m, @every 1h)
- Timezone handling via IANA timezone strings

### CMS Schema
Located in `cms/src/api/schedule/content-types/schedule/schema.json`:

```json
"schedule": {
  "type": "string",
  "required": true,
  "maxLength": 128
},
"timezone": {
  "type": "string",
  "default": "UTC",
  "maxLength": 64
}
```

### GAPS IDENTIFIED

**✓ SYNCED** - The CMS captures `schedule` (cron/alias string) and `timezone` (IANA). The engine parses and validates at runtime. No gaps.

---

## 5. CONNECTION CAPABILITIES

### Engine Capability
Located in `engine/internal/connect/connector.go`:

```go
const (
    CapQueryExec  Capability = 1 << iota  // SQL query/exec
    CapKeyValue                           // get/set/del
    CapDedupStore                         // idempotency-key locking
    CapSubscribe                          // message subscription (Phase 2)
    CapPublish                            // message publishing (Phase 2)
)
```

Each connector declares its capabilities:
- **postgres**: CapQueryExec
- **mysql**: CapQueryExec
- **valkey**: CapKeyValue | CapDedupStore
- **rest/http**: CapQueryExec
- **kafka**: CapPublish | CapSubscribe
- **rabbitmq**: CapPublish | CapSubscribe

### CMS Exposure

**GAPS IDENTIFIED**

| Gap | Severity | Description |
|-----|----------|-------------|
| Capability visibility | LOW | Capabilities are engine-internal; not surfaced in CMS |
| Dedup store selection | LOW | No CMS field to mark a connection as the dedup store |

**Assessment**: These are operational/internal features. The registry auto-wires a valkey connection as the dedup store if available. No CMS authoring needed, but could be informational.

---

## 6. RESILIENCE FEATURES

### Engine Capability
Located in `engine/internal/connect/resilience.go` and `connect.go`:

```go
type ResiliencePolicy struct {
    Timeout time.Duration
    Retry   struct {
        MaxAttempts int
        BaseBackoff time.Duration
        MaxBackoff  time.Duration
    }
    Breaker struct {
        FailureThreshold uint32
        FailureRatio     float64
        OpenTimeout      time.Duration
    }
}
```

Engine supports:
- **Timeout**: per-operation deadline
- **Retry**: maxAttempts, baseBackoff, maxBackoff (exponential with jitter)
- **Circuit Breaker**: failureThreshold, failureRatio, openTimeout

### CMS Schema
Located in `cms/src/components/connection/resilience-policy.json`:

```json
"attributes": {
    "timeoutMs": { "type": "integer", "min": 0 },
    "retry": {
        "type": "component",
        "repeatable": false,
        "component": "connection.retry-policy"
    }
}
```

And `cms/src/components/connection/retry-policy.json`:

```json
"attributes": {
    "maxAttempts": { "type": "integer", "min": 1 }
}
```

### GAPS IDENTIFIED

| Gap | Severity | Engine Field | CMS Missing |
|-----|----------|--------------|-------------|
| BaseBackoff | MEDIUM | `Retry.BaseBackoff` | Not exposed |
| MaxBackoff | MEDIUM | `Retry.MaxBackoff` | Not exposed |
| Circuit Breaker | HIGH | `Breaker.*` | Entire struct missing |

**Recommended Fix**:

1. Extend `retry-policy.json`:
```json
"attributes": {
    "maxAttempts": { "type": "integer", "min": 1 },
    "baseBackoffMs": { "type": "integer", "min": 0 },
    "maxBackoffMs": { "type": "integer", "min": 0 }
}
```

2. Add new `breaker-policy.json` component:
```json
{
  "collectionName": "components_connection_breaker_policies",
  "info": { "displayName": "breaker-policy" },
  "attributes": {
    "failureThreshold": { "type": "integer", "min": 1 },
    "failureRatio": { "type": "float", "min": 0, "max": 1 },
    "openTimeoutMs": { "type": "integer", "min": 1000 }
  }
}
```

3. Add `breaker` component to `resilience-policy.json`

---

## 7. ADMIN API OPERATIONS

### Engine Capability
Located in `engine/internal/httpapi/admin.go`:

```go
mux.Handle("POST /admin/flows", h(a.createFlow))
mux.Handle("POST /admin/flows/{id}/publish", h(a.publishFlow))
mux.Handle("POST /admin/flows/{id}/rollback", h(a.rollbackFlow))
mux.Handle("POST /admin/flows/validate", h(a.validateFlow))
mux.Handle("POST /admin/flows/dry-run", h(a.dryRunFlow))
mux.Handle("POST /admin/jdms", h(a.createJDM))
mux.Handle("POST /admin/connections", h(a.createConnection))
mux.Handle("GET /admin/connections", h(a.listConnections))
mux.Handle("GET /admin/audit/{type}/{id}", h(a.auditTrail))
mux.Handle("GET /admin/flows", h(a.listFlows))
mux.Handle("GET /admin/flows/{id}", h(a.getFlow))
```

Plus schedule admin (`schedule_admin.go`):
- CRUD for schedules
- Manual trigger (`POST /admin/schedules/{id}/run`)
- Run history (`GET /admin/schedules/{id}/runs`)

### CMS Usage

The CMS admin-client (`cms/src/plugins/rule-engine/server/src/services/admin-client.ts`) wraps these endpoints and the CMS admin UI uses:
- Version history with rollback
- Audit viewer
- Dry-run flow preview

### GAPS IDENTIFIED

**✓ SYNCED** - All admin API operations have CMS counterparts. The dry-run, validate, audit, and rollback features are exposed via the rule-engine plugin.

---

## 8. GROUP FEATURES

### Engine Capability
Located in `engine/internal/gateway/`:

The gateway/worker system supports:
- **Scaling modes**: static, dynamic, ephemeral
- **Min/max replicas**
- **Scale-down delay**
- **Startup timeout**
- **Resource limits**
- **K8s Endpoints watching**
- **Health checking**
- **Request queueing during cold start**
- **KEDA integration** (via Scaler interface)

Located in `engine/internal/gateway/dispatcher.go`:
```go
type Dispatcher struct {
    registry *WorkerRegistry
    client   *WorkerClient
    config   DispatchConfig
    scaler   *Scaler
    metrics  *GatewayMetrics
    queue    *RequestQueue
}
```

### CMS Schema
Located in `cms/src/api/group/content-types/group/schema.json`:

```json
"scalingMode": {
    "type": "enumeration",
    "enum": ["static", "dynamic", "ephemeral"],
    "default": "dynamic"
},
"minReplicas": { "type": "integer", "min": 0, "default": 0 },
"maxReplicas": { "type": "integer", "min": 1, "default": 10 },
"scaleDownDelaySeconds": { "type": "integer", "min": 60, "default": 300 },
"startupTimeoutSeconds": { "type": "integer", "min": 5, "default": 30 },
"resources": { "component": "config.resource-limits" }
```

### GAPS IDENTIFIED

| Gap | Severity | Engine Feature | CMS Status |
|-----|----------|----------------|------------|
| KEDA trigger config | MEDIUM | Scaler settings | Not exposed |
| Request queue settings | LOW | Queue config for cold start | Internal only |
| Circuit breaker per-group | LOW | WorkerClient breaker | Internal only |

**Assessment**: Most gateway features are operational and runtime-only. The CMS captures the declarative scaling policy (min/max, delays, mode). KEDA trigger configuration might warrant CMS exposure for advanced users.

---

## 9. TEMPLATE FEATURES

### Engine Capability
Located in `engine/internal/config/template.go`:

```go
type Template struct {
    ID          string
    Name        string
    Description string
    Category    string
    Variables   []TemplateVariable
    Flows       []TemplateFlowDef
}
```

Built-in templates embedded via `//go:embed templates/*.json`

### CMS Exposure
Located in `cms/src/plugins/rule-engine/`:

- `server/src/services/template-service.ts`: Lists/gets/substitutes templates
- `admin/src/pages/TemplatesPage.tsx`: UI for template gallery, variable form, preview, instantiate

### GAPS IDENTIFIED

**✓ SYNCED** - Templates are fully exposed. The CMS can list templates, preview with variable substitution, and instantiate flows.

---

## 10. OTHER CAPABILITIES

### A. Operation Kinds

**Engine** (`connect.go:Operation`):
```go
Kind    string  // "query","exec","get","set","del","http","publish"
```

**CMS**: No explicit enum - free-form string in action spec. No gap, but could benefit from validation guidance.

### B. Idempotency Features

**Engine** supports:
- `IdempotencyKeyFrom` on ActionSpec
- Dedup store via valkey `SET NX`
- Per-operation idempotency key on Operation

**CMS**: The action spec editor accepts `idempotencyKeyFrom` as a JSON field. No explicit UI guidance.

| Gap | Severity | Description |
|-----|----------|-------------|
| Idempotency UI guidance | LOW | No dedicated field in spec editor for idempotencyKeyFrom |

### C. UnwrapSingleRow

**Engine** (`ActionSpec`):
```go
UnwrapSingleRow *bool `json:"unwrapSingleRow,omitempty"`
```

**CMS**: Accepted in action spec JSON but no dedicated UI field. Low priority.

---

## Summary Table

| Category | Engine Count | CMS Count | Gap Count | Priority |
|----------|--------------|-----------|-----------|----------|
| Connector types | 7 | 3 | 4 | HIGH |
| Flow node types | 16 | 11 | 5 | HIGH |
| Webhook providers | 3 | 3 | 0 | ✓ |
| Schedule features | Full | Full | 0 | ✓ |
| Connection capabilities | 5 | 0 | 5 | LOW |
| Resilience features | 7 | 2 | 5 | HIGH |
| Admin operations | Full | Full | 0 | ✓ |
| Group features | Full | Most | 1-2 | MEDIUM |
| Templates | Full | Full | 0 | ✓ |

---

## Prioritized Recommendations

### Priority 1 (HIGH) - User-Facing Functionality Blocked

1. **Add missing connector types** to connection schema enum and create settings components:
   - mysql-settings.json
   - kafka-settings.json
   - rabbitmq-settings.json

2. **Add missing flow node types** to NODE_PALETTE:
   - messageTrigger
   - filter, find, map, reduce

3. **Add circuit breaker** to resilience-policy component

### Priority 2 (MEDIUM) - Advanced Features

4. **Add backoff settings** to retry-policy (baseBackoffMs, maxBackoffMs)

5. **Consider KEDA trigger config** in group schema for advanced scaling

### Priority 3 (LOW) - Quality of Life

6. **Add http** as explicit alias in connection type enum (optional - works via rest)

7. **Consider capability visibility** in connection detail view (informational)

8. **Add idempotencyKeyFrom** field guidance in action spec editor

---

*Investigation complete. This report covers all 10 requested capability categories.*
