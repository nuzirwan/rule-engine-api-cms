# Low-Level Design — nzr-rules-engine

The implementable blueprint for the v1 Go engine (data plane). It composes five independently
designed slices into one coherent design, over the shared contracts in
[`lld-contracts.md`](./lld-contracts.md), realizing the approved HLD [`hld.md`](./hld.md).

Status: **draft for build** · Module `nzr-rules-engine` · Go 1.22+ (stdlib `net/http`) ·
Last updated: 2026-10-02 · Follows `[[low-level-design]]` (interfaces at boundaries, SOLID,
class/sequence diagrams, planned error handling, tests-first).

## How to read this document
- This file = the **map**: package layout, how slices fit, cross-cutting flows, AC coverage.
- Each slice has a **detailed** LLD (types, pseudocode, per-package diagrams, test plans):
  - [Slice A — Flow schema + interpreter + context accumulator](./lld/slice-a-interpreter.md) (`internal/flow`, `internal/flow/saga`)
  - [Slice B — Connection Registry + connectors + resilience](./lld/slice-b-connections.md) (`internal/connect`)
  - [Slice C — ZEN integration + AuthN/AuthZ](./lld/slice-c-zen-auth.md) (`internal/decision`, `internal/auth`)
  - [Slice D — Config store + versioning + cache + admin](./lld/slice-d-configstore.md) (`internal/config`)
  - [Slice E — Observability + ops + lifecycle](./lld/slice-e-observ-ops.md) (`internal/observ`, ops in `internal/httpapi`, `cmd/engine`)
  - [Slice F — Scheduler](./lld/slice-f-scheduler.md) (`internal/scheduler`) — background time-based flow execution
  - [Slice G — Worker](./lld/slice-g-worker.md) (`internal/worker`, `cmd/worker`) — group-isolated runtime
  - [Slice H — Webhook](./lld/slice-h-webhook.md) (`internal/webhook`) — external event ingestion
  - [Slice I — Gateway](./lld/slice-i-gateway.md) (`internal/gateway`) — request dispatcher with auto-scaling
- The frozen interfaces every slice depends on live in [`lld-contracts.md`](./lld-contracts.md).

## 1. Package layout (as built)

```
nzr-rules-engine/
  cmd/engine/            main: wire deps, start server, graceful shutdown (Slice E)
  cmd/worker/            worker binary: group-isolated runtime for multi-tenant deployments
  internal/
    flow/                Slice A — node specs, interpreter, Ctx accumulator, validation, budgets
      saga/              saga/compensation coordinator for cross-source writes (R3 mitigation)
    connect/             Slice B — Registry, resilientClient, resilience, secret, dedup guard (R4)
      drivers/           postgres (pgx), valkey, rest/http (net/http)
    decision/            Slice C — ZEN embed (CGO), compiled-JDM cache, Evaluator
    auth/                Slice C — AuthN (JWT/JWKS), AuthZ (ZEN decision)
    config/              Slice D — Store, versioning, cache, admin validate/dry-run
    observ/              Slice E — Tracer/Span/Logger, per-node trace, metrics, trace collector
    httpapi/             router + middleware chain + ops endpoints (Slice E owns ops) + rate limiting (R6)
    scheduler/           Slice F — background scheduler for time-based flow execution
    worker/              Slice G — group-isolated worker runtime (LoadGroup, Execute, hot-reload)
    webhook/             Slice H — webhook receiver (providers: GitHub, Stripe, generic)
    gateway/             Slice I — API gateway (dispatcher, scaler, queue, worker registry)
    envfile/             utility — dependency-free .env loader
  migrations/            Slice D — SQL migrations (golang-migrate)
```

**Verified 2026-10-07:** `ls engine/internal/` confirms: auth, config, connect, decision, envfile, flow, gateway, httpapi, observ, scheduler, webhook, worker. `ls engine/cmd/` confirms: engine, worker.

## 2. How the slices compose (dependency arrows point at abstractions)

```mermaid
flowchart TB
    subgraph httpapi["internal/httpapi — edge"]
      RT["Router (net/http ServeMux)"]
      MW["Middleware: AuthN → AuthZ → resolve+pin → trace scope"]
      OPS["/livez /readyz /metrics"]
    end
    A["internal/flow\nInterpreter (recursive walk)"]
    B["internal/connect\nRegistry + resilientClient"]
    C1["internal/decision\nZEN Evaluator (CGO)"]
    C2["internal/auth\nAuthenticator + Authorizer"]
    D["internal/config\nStore + versioning + cache"]
    E["internal/observ\nTracer/Logger/metrics/collector"]

    RT --> MW --> A
    MW --> C2
    C2 --> C1
    A -->|Deps.Conns| B
    A -->|Deps.Decide| C1
    A -->|Deps.Trace/Log| E
    C1 -->|JDMLoader adapter| D
    MW -->|ActiveFlow resolve+pin| D
    B -->|HealthCheck| OPS
    D -->|Ping| OPS
    B -. SecretProvider .-> SEC["cmd/engine wires SecretProvider"]
    E -. per-node spans .-> A

    classDef a fill:#1f6feb,color:#fff; classDef b fill:#2ea043,color:#fff
    classDef c fill:#8957e5,color:#fff; classDef d fill:#9e6a03,color:#fff; classDef e fill:#b45309,color:#fff
    class A a; class B b; class C1,C2 c; class D d; class E e
```

Key seams (all in [`lld-contracts.md`](./lld-contracts.md)): `flow` reaches every other slice
only through `Deps` (`connect.Registry`, `decision.Evaluator`, `observ.Tracer/Logger`); `decision`
reaches the store only through the `JDMLoader` port; `auth` reuses the same `decision.Evaluator`
seam for AuthZ; ops endpoints gate on `Registry.HealthCheck` + `Store.Ping`.

## 3. End-to-end request flow (all slices)

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant MW as httpapi middleware
    participant AU as auth (AuthN/AuthZ, Slice C)
    participant ST as config.Store (Slice D)
    participant IP as flow.Interpreter (Slice A)
    participant DE as decision ZEN (Slice C)
    participant CN as connect.Registry (Slice B)
    participant OB as observ (Slice E)

    C->>MW: HTTP request (Bearer JWT)
    MW->>AU: Authenticate(bearer) → Principal
    MW->>AU: Authorize({user,roles,resource,action}) via ZEN
    AU->>DE: Evaluate(authzJDM, input)
    DE-->>AU: {allow:true}
    MW->>ST: ActiveFlow(env,method,path)  %% resolve + PIN version (AC-11)
    ST-->>MW: FlowVersion (immutable snapshot)
    MW->>OB: EnrichScope(flow_id, version, env) ; start trace_id
    MW->>IP: Run(FlowVersion, Ctx, Deps)
    loop recursive walk (each node wrapped in a span)
      IP->>DE: Evaluate(condJDM, ctxSubset)  %% condition picks branch
      DE-->>IP: {branch}
      IP->>CN: Client(key).Execute(op)  %% action node I/O (timeout+breaker+retry)
      CN-->>IP: result → ctx.Data
      IP->>IP: Set targetPath → ctx.Response (no-clobber stitch)
    end
    IP-->>MW: ctx.Response
    MW-->>C: stitched JSON (trace flushed)
```

## 4. Cross-cutting invariants (how the slices stay consistent)

- **Version pinning (AC-11).** The middleware resolves+pins `FlowVersion` once (Slice D); the
  interpreter walks that immutable snapshot by value (Slice A); `decision` keys its compiled-JDM
  cache by `(id,version)` (Slice C). A mid-flight publish/rollback never tears a request.
- **One `trace_id` everywhere (AC-21).** `observ` (Slice E) issues the id in middleware; Slice A
  wraps every node in a span; Slice C and Slice B calls inherit the ctx, so a slow source is
  attributable to the exact node+flow. W3C propagation to downstream HTTP.
- **Pure decisions, impure orchestration (ADR-001).** Every condition/authz/computed value goes
  through `decision.Evaluator` (JSON in/out, no I/O); all I/O is Slice B behind the registry.
- **Secrets never leak (AC-20).** `SecretProvider` (contract) resolves `SecretRef`; `Secret`
  redacts on String/JSON; Slice C/E redactors drop sensitive keys before any sink.
- **Bad config never 500s (AC-15).** Slice A `ValidateTree` + strict spec decode run at publish
  (Slice D) and defensively on load; unknown fields/types reject cleanly (R11 forward-compat).
- **Error taxonomy across seams.** Every slice returns `Timeout/NotFound/Validation/Upstream/
  Internal` classified errors (`[[error-classification]]`); callers branch via `errors.Is/As`.

## 5. Reconciled seam changes

All five slices designed against the frozen contracts; the requested changes were **additive and
non-conflicting** (full list in [`lld-contracts.md`](./lld-contracts.md) → "Reconciled seam
changes"): `Operation` override/idempotency/required fields; `SecretProvider` promoted to the
contract; `config.Store.Ping`; `decision.JDMLoader` port + `Engine.Close()`; `auth.*` interfaces;
`observ.TraceCollector` + context helpers + `EnrichScope` (dry-run collector is context-carried,
so `Deps` is unchanged); env-on-context for per-env JDM resolution.

**Gaps the consistency pass + review caught and fixed:**
- **Dry-run write-suppression (gap):** all three of D/E/A referenced a dry-run "write-suppression
  flag" but none owned it, and Slice A's doc didn't act on it (its `actionHandler` would have run
  real writes during dry-run). Resolved on the context — `observ.WithDryRun(ctx)` /
  `IsDryRun(ctx)` — with an explicit **Slice A obligation**: `actionHandler` checks `IsDryRun(ctx)`
  and, for a write op, skips the I/O and records `wrote:"suppressed"` (AC-14).
- **Recursion seam (review #2):** replaced Slice A's internal `childWalker` func-pointer workaround
  with a first-class **`Walker`** interface in the contract; `NodeHandler.Exec` now takes a
  `Walker` and the `Interpreter` is its sole implementer — no handler→interpreter import cycle.
- **ResiliencePolicy fields** fixed in the contract (Slice B req 2) for field-level override merge.

All recorded in [`lld-contracts.md`](./lld-contracts.md) "Reconciled seam changes". No conflicts
required arbitration.

## 6. Acceptance-criteria coverage map (HLD §10)

Every HLD AC maps to the slice(s) that satisfy and test it. "Done" = all v1 ACs pass.

| AC | What | Slice(s) |
|---|---|---|
| AC-S1 | CGO build of zen-go in target image | C (+ CI) |
| AC-S2 | Hardest condition as JDM evaluates correctly | C |
| AC-S3 | One nested flow end-to-end (PG+REST) | A+B+C+D |
| AC-1 | Route resolves/stitches; 404 unmatched | A, httpapi |
| AC-2 | Correct branch per depth; branch observable | A, E |
| AC-3 | Conditional response field; no clobber | A |
| AC-4 | ZEN deterministic per (id,version,input) | C |
| AC-5 | One pooled client per connection key, reused | B |
| AC-6 | Breaker opens, fails fast, isolates source | B |
| AC-7 | Per-node resilience override precedence | B (+ A spec) |
| AC-8 | New source type via Connector, no engine change | B |
| AC-9 | Edit creates immutable new version | D |
| AC-10 | Publish advances pointer; rollback moves back | D |
| AC-11 | Request pins to version at start | D (resolve) + A (walk) |
| AC-12 | Promotion carries version + fixtures | D |
| AC-13 | validate runs fixtures; blocks publish on fail | D (+ A ValidateTree) |
| AC-14 | dry-run returns trace, writes suppressed | D + E (collector) + A |
| AC-15 | Malformed config rejected on load, no 500 | A + D |
| AC-16 | Cache invalidation across instances | D |
| AC-17 | Loop/ForEach bounds; no hang | A |
| AC-18 | JWT/JWKS; rotation without restart | C (auth) |
| AC-19 | AuthZ allow/deny via ZEN | C (auth) |
| AC-20 | No secrets in logs/traces/versions | B + C + E |
| AC-21 | Single trace_id spanning walk/ZEN/connectors | E (+ A spans) |
| AC-22 | Logger node emits fields; config toggle | A (handler) + E |
| AC-23 | /livez, /readyz gated on registry+store | E (+ B.HealthCheck, D.Ping) |
| AC-24 | Graceful shutdown drains + closes pools | E (cmd/engine) |
| AC-25 | CI green, CGO-aware | all (+ CI) |
| AC-26 | Example flow runs without the CMS | A+B+C+D (+ seed) |
| AC-27 | Cross-source writes non-atomic, classified | B |
| AC-28 | No-rate-limit / per-instance breaker documented | B (docs) |
| AC-29 | Filter node processes collection with ZEN predicate, respects maxItems | A |
| AC-30 | Find node returns first matching item | A |
| AC-31 | Saga coordinator runs compensations in reverse order on failure | A (saga) |
| AC-32 | Rate limiter returns 429 with Retry-After when exceeded | E (httpapi) |
| AC-33 | Scheduler executes flows on cron schedule | F |
| AC-34 | Webhook receiver verifies signatures and triggers flows | H |
| AC-35 | Worker loads group config and executes flows in isolation | G |
| AC-36 | Gateway dispatches requests to workers with auto-scaling | I |

Gaps / documented constraints carried from the HLD: AC-27 (non-atomic cross-source writes —
**mitigated by saga/compensation, R3**), AC-28 (per-instance breakers — unchanged; **rate limiting
implemented, R6**) — honest limits, not bugs. Added at LLD
(review #5): **cross-instance activation is eventually consistent** — after a publish/rollback,
the fleet converges within one pub/sub round-trip (TTL-bounded if a message is missed), so two
instances may briefly serve different active versions. Each request is internally consistent
(pinned, AC-11); the fleet is not instantaneously uniform. Callers treat publish as asynchronous.
Full statement in [Slice D §4](./lld/slice-d-configstore.md).

## 7. Build order (suggested, respects dependencies)

1. **Phase 0 spike** (AC-S1..S3) — Slice C CGO build + one nested flow (A+B+C+D minimal). Gate.
2. Contracts + Slice D store/schema (the config contract everything reads).
3. Slice B registry/connectors (I/O the interpreter needs).
4. Slice A interpreter + validation (the core walk).
5. Slice C decision + auth (decisions the walk calls).
6. Slice E observ + ops + cmd/engine lifecycle (wrap it all).
7. End-to-end example flow (AC-26) + CI (AC-25).

The Phase-0 spike remains the go/no-go gate before committing to the full v1 build.
