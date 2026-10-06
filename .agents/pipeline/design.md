# Design: Idempotency Keys for Non-Idempotent Write Operations (R4)

## Summary

Implement automatic idempotency-key generation and attachment for non-idempotent HTTP POST/PUT
calls to external services, enabling safe retries under at-least-once semantics per
`[[idempotency-and-dedup]]` and `[[at-least-once-processing]]`.

## Standards Applied

| Standard | Application |
|----------|-------------|
| `[[idempotency-and-dedup]]` | Key format (sortable), dedup lock with TTL, pessimistic posture on store error, release-on-failure rule |
| `[[at-least-once-processing]]` | At-least-once + idempotent handling is the default; the key makes non-idempotent writes safe to retry |
| `[[simplicity-and-design]]` | Smallest change: reuse existing `Operation.IdempotencyKey` and `dedupGuard` infrastructure; no new abstractions |
| `[[low-level-design]]` | Interface segregation: ActionSpec gets a new `IdempotencyKeySpec` field; generation logic stays in flow layer; connector receives the key via existing `Operation.IdempotencyKey` |
| `[[high-level-design]]` | Per R4 in docs/hld.md: "Idempotency key derived from request + dedup table/TTL" |

## Context

The engine already has:
1. `Operation.IdempotencyKey` field (connect/connect.go:46) - accepted by resilientClient
2. `isIdempotent(op)` logic (connect/resilience.go:109) - marks keyed ops as retryable
3. `dedupGuard` + `DedupStore` (connect/dedup.go) - atomic SET-NX lock in Valkey with TTL
4. REST connector adds headers from payload (drivers/rest.go:112) - `stringMapSetting(p, "headers")`

What's missing:
- **Key generation**: deterministic key from request context (flow_id, request_id, node_id)
- **ActionSpec integration**: way to opt-in idempotency per action node
- **Automatic header attachment**: `Idempotency-Key` header for HTTP calls

## Design

### 1. Key Format

Per `[[idempotency-and-dedup]]`, use a sortable, deterministic scheme:

```
{flow_id}:{request_id}:{node_id}
```

Example: `flow-orders:req-01J8XYZ:action-create-payment`

This ensures:
- **Determinism**: same request retried produces same key (replay-safe)
- **Sortability**: keys group by flow/request for debugging
- **Uniqueness**: node_id scopes within a flow execution

### 2. ActionSpec Extension

Add an optional `idempotencyKey` field to `ActionSpec`:

```go
// flow/spec.go
type ActionSpec struct {
    ConnRef
    Operation      connect.Operation   `json:"operation"`
    SaveAs         string              `json:"saveAs"`
    Resilience     *ResilienceOverride `json:"resilience,omitempty"`
    OnError        OnError             `json:"onError,omitempty"`
    IdempotencyKey *IdempotencyKeySpec `json:"idempotencyKey,omitempty"` // NEW
}

// IdempotencyKeySpec configures idempotency for an action node.
type IdempotencyKeySpec struct {
    // Enabled turns on idempotency-key generation for this action.
    // Default false (backwards compat).
    Enabled bool `json:"enabled"`
    // HeaderName is the HTTP header to attach (default "Idempotency-Key").
    HeaderName string `json:"headerName,omitempty"`
}
```

### 3. Key Generation (flow/idempotency.go)

New file in `internal/flow/`:

```go
// idempotency.go - Idempotency key generation for action nodes (R4)

package flow

import "fmt"

// DefaultIdempotencyHeader is the standard header name per RFC draft-ietf-httpapi-idempotency-key.
const DefaultIdempotencyHeader = "Idempotency-Key"

// GenerateIdempotencyKey builds a deterministic key from the execution context.
// Format: {flow_id}:{request_id}:{node_id} per [[idempotency-and-dedup]].
func GenerateIdempotencyKey(flowID, requestID, nodeID string) string {
    return fmt.Sprintf("%s:%s:%s", flowID, requestID, nodeID)
}
```

### 4. Context Fields (flow/ctx.go)

Current `Ctx` has `RequestID` but NOT `FlowID`. The FlowID lives in `Version` passed to `Run()`.

**Solution**: Add `FlowID` to `Ctx`:

```go
// flow/ctx.go
type Ctx struct {
    FlowID    string         // NEW: set by Interpreter.Run() from Version.FlowID
    RequestID string         // from httpapi (X-Request-Id header)
    TraceID   string
    Env       string
    Input     map[string]any
    Data      map[string]any
    Response  map[string]any
}
```

**Change in interpreter.go `Run()`**:
```go
func (ip *Interpreter) Run(ctx context.Context, tree *Node, ver Version, c *Ctx, dep Deps) error {
    c.FlowID = ver.FlowID  // NEW: populate FlowID from Version
    // ... rest unchanged
}
```

This is minimal: one field add, one assignment line.

### 5. Handler Integration (flow/handlers.go)

Modify `actionHandler.Exec` to:
1. Check if `spec.IdempotencyKey != nil && spec.IdempotencyKey.Enabled`
2. Generate key from `c.FlowID`, `c.RequestID`, `n.ID`
3. Set `op.IdempotencyKey` on the operation
4. For HTTP ops: inject the header into `op.Payload["headers"]`

```go
// In actionHandler.Exec, after parsing spec and before client.Execute:

op := spec.Operation
if spec.IdempotencyKey != nil && spec.IdempotencyKey.Enabled {
    key := GenerateIdempotencyKey(c.FlowID, c.RequestID, n.ID)
    op.IdempotencyKey = key
    
    // For HTTP ops, also attach the header so downstream services receive it.
    if op.Kind == "http" {
        headerName := spec.IdempotencyKey.HeaderName
        if headerName == "" {
            headerName = DefaultIdempotencyHeader
        }
        // Clone payload to avoid mutating the spec's original map
        op.Payload = clonePayload(op.Payload)
        headers := ensureHeaders(op.Payload)
        headers[headerName] = key
    }
}
```

Helper functions:
```go
// clonePayload returns a shallow copy of p so mutations don't affect the spec.
func clonePayload(p map[string]any) map[string]any {
    if p == nil {
        return map[string]any{}
    }
    out := make(map[string]any, len(p))
    for k, v := range p {
        out[k] = v
    }
    return out
}

// ensureHeaders returns or creates the headers map in payload.
func ensureHeaders(p map[string]any) map[string]any {
    if h, ok := p["headers"].(map[string]any); ok {
        // Clone headers too to avoid mutating spec
        out := make(map[string]any, len(h)+1)
        for k, v := range h {
            out[k] = v
        }
        p["headers"] = out
        return out
    }
    h := make(map[string]any)
    p["headers"] = h
    return h
}
```

### 6. Data Flow

```
Request arrives (httpapi)
    ↓ generates request_id (X-Request-ID header)
Interpreter.Run(flow_id, ctx)
    ↓ c.FlowID = ver.FlowID (NEW)
    ↓ ctx.RequestID already set by httpapi
actionHandler.Exec()
    ↓ spec.IdempotencyKey.Enabled = true
    ↓ key = GenerateIdempotencyKey(c.FlowID, c.RequestID, node.ID)
    ↓ op.IdempotencyKey = key
    ↓ op.Payload["headers"]["Idempotency-Key"] = key
client.Execute(op)
    ↓ isIdempotent(op) = true (because op.IdempotencyKey != "")
    ↓ retry loop enabled, dedup guard activated (if Valkey configured)
REST driver.do(op)
    ↓ reads headers from op.Payload["headers"]
    ↓ attaches "Idempotency-Key: {key}" to outbound request
```

### 7. Files Changed

| File | Change |
|------|--------|
| `internal/flow/spec.go` | Add `IdempotencyKeySpec` struct and field to `ActionSpec` |
| `internal/flow/idempotency.go` | New file: `GenerateIdempotencyKey()`, `DefaultIdempotencyHeader`, `clonePayload()`, `ensureHeaders()` |
| `internal/flow/handlers.go` | Modify `actionHandler.Exec` to generate and attach key |
| `internal/flow/ctx.go` | Add `FlowID` field to `Ctx`, update `NewCtx()` and `cloneWritable()` |
| `internal/flow/interpreter.go` | Set `c.FlowID = ver.FlowID` in `Run()` |
| `internal/flow/idempotency_test.go` | New file: unit tests for key generation and helpers |
| `internal/flow/handlers_test.go` | Add tests for idempotency integration |

### 8. What Stays Unchanged

Per `[[simplicity-and-design]]`, reuse existing infrastructure:
- `connect.Operation.IdempotencyKey` - already wired
- `isIdempotent()` / `isNaturallyIdempotent()` - already checks this field
- `resilientClient.callInner()` - already guards with `dedupGuard` when key present
- `dedupGuard` / `DedupStore` - already implements pessimistic SET-NX lock
- REST driver header handling - already reads from `payload["headers"]`

No changes needed in `internal/connect/` at all.

## Test Plan

### Unit Tests (New)

1. **idempotency_test.go**
   - `TestGenerateIdempotencyKey`: key format is `{flowID}:{requestID}:{nodeID}`
   - `TestGenerateIdempotencyKey_EmptyInputs`: handles empty strings (produces `::nodeID`)
   - `TestClonePayload`: verifies shallow copy doesn't share map identity
   - `TestEnsureHeaders`: creates headers map if absent, clones if present

2. **handlers_test.go** (additions)
   - `TestActionHandler_IdempotencyKeyEnabled`: op.IdempotencyKey is set, header injected for HTTP
   - `TestActionHandler_IdempotencyKeyDisabled`: no key/header when disabled (default)
   - `TestActionHandler_IdempotencyKeyCustomHeader`: custom header name respected
   - `TestActionHandler_IdempotencyKeyNonHTTP`: non-HTTP ops still set op.IdempotencyKey but no header

3. **ctx_test.go** (additions)
   - `TestCtx_FlowID`: verify FlowID is properly cloned in `cloneWritable()`

### Regression Set

Existing tests that must pass (verify no breakage):
- `internal/connect/resilience_test.go` - existing idempotency key handling
- `internal/flow/interpreter_test.go` - flow execution
- `internal/flow/validate_test.go` - spec validation (unknown fields rejected)
- `internal/flow/handlers_test.go` - existing handler tests
- `internal/flow/ctx_test.go` - existing ctx tests

### Build/Test Commands

```bash
cd engine
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./... -short  # skip integration tests
```

## Not in Scope

Per `[[simplicity-and-design]]` YAGNI:
- **Configurable key format**: the standard format covers all use cases
- **Per-connection idempotency settings**: action-level is sufficient
- **Custom dedup TTL per action**: connection-level TTL via registry is sufficient
- **Valkey dedup store setup**: already wired via `registry.wireDedup()`

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Payload mutation affecting spec | Clone payload and headers maps before mutation |
| Header name collision | Use standard `Idempotency-Key`; custom name opt-in |
| Empty RequestID | Still produces valid key `flowID::nodeID`; deterministic |

## Open Questions (None)

The existing infrastructure handles all concerns:
- Dedup lock: `dedupGuard` with Valkey SET-NX
- Retry safety: `isIdempotent()` checks `IdempotencyKey`
- Release-on-failure: `dedupGuard.guard()` calls `Release()` on error
- Pessimistic posture: store error blocks the write

---

Design complete. Ready for implementation.
