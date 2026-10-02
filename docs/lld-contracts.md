# LLD Shared Contracts — nzr-rules-engine (the seams between slices)

This file defines the **package layout** and the **core Go interfaces/types** that every LLD
slice designs against. Treat these signatures as fixed seams: a slice may propose a change, but
must flag it as a seam change (it affects other slices). Go 1.22+. Module path `nzr-rules-engine`.

Follows [[low-level-design]] (interfaces at module boundaries, DIP, high cohesion / low coupling)
and the HLD in `docs/hld.md`.

## Package layout

```
nzr-rules-engine/
  cmd/engine/            main: wire deps, start server, graceful shutdown
  internal/
    flow/                Slice A — node types, schema, interpreter, ctx accumulator
    connect/             Slice B — Connection Registry, Connector/Client, resilience
      drivers/           postgres, valkey, rest
    decision/            Slice C — ZEN integration (JDM load/eval)
    auth/                Slice C — AuthN (JWT/JWKS), AuthZ (ZEN policy)
    config/              Slice D — config store, versioning, cache, admin validate/dry-run
    observ/              Slice E — tracing, logging, metrics, Logger node support
    httpapi/             Slice E/A — router, middleware chain, ops endpoints
  migrations/            Slice D — SQL migrations
```

## Core shared types & interfaces (the contract)

### Execution context (accumulator) — owned by Slice A, used by all
```go
// Ctx is the per-request accumulator threaded through the whole flow walk.
// Not safe for concurrent writes except within a Parallel node's merge step.
type Ctx struct {
    RequestID string
    TraceID   string
    Env       string
    Input     map[string]any   // request body/params/headers (read-only after trigger)
    Data      map[string]any    // gathered source data, keyed by node output
    Response  map[string]any    // finalResponse; written only via SetPath
}

// SetPath mounts v at a dotted targetPath into Response (lodash.set semantics,
// create-intermediate, never object-merge-overwrite siblings).
func (c *Ctx) SetPath(targetPath string, v any) error
// GetPath reads a dotted path from the merged view (Input+Data+Response).
func (c *Ctx) GetPath(path string) (any, bool)
```

### Node + interpreter — Slice A
```go
type NodeType string // "trigger","action","condition","switch","sequence",
                      // "parallel","forEach","decision","set","logger","response"

// Node is one node in the flow tree. Control nodes carry Children.
type Node struct {
    ID       string
    Type     NodeType
    Spec     json.RawMessage // type-specific config (parsed per handler)
    Children []Node          // for control nodes
}

// NodeHandler executes one node, mutating ctx, returning the next directive.
// Control-node handlers descend into children via the Walker passed in — NOT by importing the
// Interpreter (that inverts the dependency and removes the handler→interpreter import cycle;
// stitch fix 2026-10-02, review item #2). Leaf handlers ignore w.
type NodeHandler interface {
    Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error)
}

// Walker is the single recursion seam: a handler calls w.Walk(child) to execute a child subtree.
// The Interpreter is the sole implementer; handlers depend only on this interface (DIP), so
// there is no handler→interpreter import cycle and no injected func pointer.
type Walker interface {
    Walk(ctx context.Context, child *Node, c *Ctx, dep Deps) (Directive, error)
}

// Directive tells the interpreter how to proceed (e.g. which child branch).
type Directive struct {
    Branch   string   // for condition/switch: selected child key ("" = linear)
    Stop     bool     // response node sets true
}

// Deps is the handler's view of the other slices (dependency inversion).
type Deps struct {
    Conns   connect.Registry
    Decide  decision.Evaluator
    Trace   observ.Tracer
    Log     observ.Logger
}
```

### Connections — Slice B (used by Slice A action nodes)
```go
type Registry interface {
    // Client returns the live pooled client for a connection key (current version).
    Client(ctx context.Context, key string) (Client, error)
    Reload(ctx context.Context, defs []ConnectionDef) error // hot-reload
    HealthCheck(ctx context.Context) error                  // for /readyz
}

type Client interface {
    Execute(ctx context.Context, op Operation) (any, error) // honors ctx + resilience
    Close() error
}

type Connector interface { // one per type: postgres, valkey, rest
    Type() string
    Open(ctx context.Context, def ConnectionDef) (Client, error)
}

type Operation struct {    // the per-node operation payload
    Kind    string            // "query","exec","get","set","del","http"
    Payload map[string]any     // query+params | key | method+path+body ...
    // --- seam additions (Slice B) ---
    Override       *ResiliencePolicy // per-node resilience override; nil => connection default (AC-7)
    IdempotencyKey string            // non-empty makes a non-idempotent write safe to retry (R4)
    Required       bool              // true => required write (abort on failure); false => best-effort (R3)
}

type ConnectionDef struct {
    Key, Type string
    Settings  map[string]any   // host/port/baseURL/pool...
    SecretRef string            // never a secret value
    Resilience ResiliencePolicy // timeout/retry/breaker (per-node override allowed)
}

// ResiliencePolicy — fields fixed here (Slice B req 2) so per-field merge can tell unset from
// zero. A nil sub-struct / zero field means "inherit"; an Operation.Override sets only the fields
// it wants to change (field-level, node wins — AC-7). The breaker is per-connection (not
// overridable per node): one breaker per key regardless of overrides (R10, keeps AC-6 coherent).
type ResiliencePolicy struct {
    Timeout time.Duration
    Retry   struct{ MaxAttempts int; BaseBackoff, MaxBackoff time.Duration }
    Breaker struct{ FailureThreshold uint32; FailureRatio float64; OpenTimeout time.Duration }
}

// SecretProvider resolves a SecretRef to a value; env-backed in v1, pluggable to Vault/SSM.
// Promoted to the contract (Slice B) because cmd/engine wires it and Slice D may share it.
type SecretProvider interface {
    Resolve(ctx context.Context, ref string) (Secret, error)
    Watch(ctx context.Context, ref string, onChange func()) (stop func(), err error)
}
type Secret struct{ /* opaque; String()/MarshalJSON redact to "***"; Reveal() []byte for drivers */ }
```

### Decisions (ZEN) — Slice C (used by condition/decision nodes)
```go
type Evaluator interface {
    // Evaluate runs a JDM (by id) over a snapshot of ctx, returns decision JSON.
    // Env rides on ctx (per-env isolation, ADR-006) — the signature is unchanged.
    Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error)
}

// JDMLoader port (seam addition, Slice C): decision depends on this abstraction, not on
// config.Store directly (DIP). Satisfied by a thin adapter over config.Store.GetJDM.
type JDMLoader interface {
    LoadJDM(ctx context.Context, env, jdmID string) (jdm []byte, version int, err error)
}
// The concrete decision.Engine exposes Close() to free Rust-side compiled graphs on shutdown;
// cmd/engine wires it directly (not part of the Evaluator seam).
```

### Auth — Slice C (used by httpapi middleware chain, Slice E)
```go
type Authenticator interface { // AuthN: validate bearer -> principal (JWT/JWKS)
    Authenticate(ctx context.Context, bearer string) (Principal, error)
}
type Authorizer interface {    // AuthZ: allow/deny as a ZEN decision
    Authorize(ctx context.Context, in AuthzInput) (Decision, error)
}
type Principal struct { Subject string; Roles []string; Claims map[string]any }
type AuthzInput struct { User string; Roles []string; Resource, Action string; Attrs map[string]any }
type Decision struct { Allow bool; Reason string } // Reason is for audit logs, never the client
```

### Config store — Slice D (used by resolver, admin, registry)
```go
type Store interface {
    // ActiveFlow resolves the active flow version for (env, method, path).
    ActiveFlow(ctx context.Context, env, method, path string) (FlowVersion, error)
    GetJDM(ctx context.Context, env, id string) (jdm []byte, version int, err error)
    Connections(ctx context.Context, env string) ([]ConnectionDef, error)
    // admin:
    PutFlowVersion(ctx context.Context, env string, f FlowVersion) (version int, err error)
    SetActive(ctx context.Context, env, flowID string, version int) error // publish/rollback
    Ping(ctx context.Context) error // seam addition (Slice E): cheap reachability for /readyz
}

type FlowVersion struct {
    FlowID   string
    Version  int
    Method, Path string
    Tree     Node          // root
    Fixtures []FlowFixture // travel with the version
}

type Cache interface { // Valkey; invalidation via pub/sub on publish
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key string, v []byte, ttl time.Duration) error
    Invalidate(ctx context.Context, keyPattern string) error // publishes change event
}
```

### Observability — Slice E (used everywhere)
```go
type Tracer interface {
    StartSpan(ctx context.Context, name string, attrs map[string]any) (context.Context, Span)
}
type Span interface { End(err error); Set(attr string, v any) }

type Logger interface { // structured; fields per HLD §3
    Emit(ctx context.Context, level, label string, fields map[string]any)
}

// Additive helpers (seam addition, Slice E) — no change to the interfaces above.
// Deps stays stable; the dry-run trace collector travels on the context (option a).
type TraceCollector interface { Record(nodeID, nodeType, branch string, attrs map[string]any) }
// func WithCollector(ctx, TraceCollector) context.Context  // dry-run attaches; live traffic nil
// func CollectorFrom(ctx) (TraceCollector, bool)
// func EnrichScope(ctx, flowID string, version int, env string) context.Context // resolver stamps scope
// func WithDryRun(ctx) context.Context  // dry-run handler (Slice D) sets this
// func IsDryRun(ctx) bool                // Slice A actionHandler checks: if true AND op is a write,
//                                        // SKIP the I/O and record wrote:"suppressed" (AC-14).
```

## Seam rules for parallel slices
- Design to these interfaces; **do not redefine** another slice's type.
- If a slice needs a new method on a shared interface, add it to a "Seam changes requested"
  section in that slice's output — the stitch step reconciles.
- Error handling crosses seams as wrapped errors classified per [[error-classification]]
  (`Timeout`, `NotFound`, `Validation`, `Upstream`, `Internal`).
- Every interface method takes `context.Context` first (cancellation/trace propagation).

## Reconciled seam changes (stitch step, 2026-10-02)
All five slices designed cleanly; the requested seam changes were **additive and
non-conflicting**. Applied above:
- **Operation** gains `Override`, `IdempotencyKey`, `Required` (Slice B; AC-7/R3/R4).
- **SecretProvider**/`Secret` promoted to the contract (Slice B; wired by cmd/engine).
- **config.Store.Ping** added for `/readyz` (Slice E/AC-23; owned by Slice D).
- **decision.JDMLoader** port added; `decision.Engine.Close()` wired by cmd/engine (Slice C).
- **auth.*** interfaces added (Slice C) for the httpapi middleware chain.
- **observ** `TraceCollector` + context helpers + `EnrichScope` added; `Deps` left unchanged
  (dry-run collector is context-carried — option a).
- **env-on-context** convention: httpapi/middleware places request env on `ctx`; `decision`
  reads it for per-env JDM resolution (ADR-006). No signature change.
- **dry-run write-suppression** (stitch-identified gap, 2026-10-02): the three slices referenced a
  "dry-run flag" but none owned it, and Slice A was silent on acting on it. Resolved on the
  **context** (consistent with the collector): Slice D's dry-run handler calls `WithDryRun(ctx)`;
  **Slice A's `actionHandler` MUST call `IsDryRun(ctx)` and, when true for a write op, skip the
  I/O and record `wrote:"suppressed"`** (AC-14). This is a required Slice A implementation
  obligation not stated in its slice doc — carried here and in `lld.md` §5.
