# Documentation-Codebase Alignment Audit

**Date:** 2026-10-07  
**Repository:** `/home/nuzirwan/project/rule-engine-api`  
**Auditor:** Kiro (automated workflow)

---

## Verification Note (2026-10-07)

**Package layout verification:**
```
ls engine/internal/
# Output: auth config connect decision envfile flow gateway httpapi observ scheduler webhook worker

ls engine/cmd/
# Output: engine worker

ls engine/internal/flow/saga/
# Output: saga.go saga_test.go
```

**Node types verification (15 spec structs in spec.go):**
- trigger, action, condition, switch, sequence, parallel, forEach, decision, set, logger, response
- filter, find, map, reduce (collection nodes - CONFIRMED)

**R3/R4/R6 implementation verification:**
- R3 saga: `engine/internal/flow/saga/saga.go` - Context struct with State, Steps, Record, Commit, Compensate
- R4 dedup: `engine/internal/connect/dedup.go` - DedupStore interface, dedupGuard struct, guard() method
- R6 rate limit: `engine/internal/httpapi/ratelimit.go` - RateLimitConfig, rateLimiter, rateLimitMiddleware

All documented features exist in code. Documentation updated to reflect actual implementation.

---

## Executive Summary

The architecture documentation (`hld.md`, `lld.md`, `lld-contracts.md`, and slice docs) was created during the original v1 design phase. Since then, significant features have been implemented that are **documented in STATE.md but NOT reflected in the architecture docs**. The docs describe the planned v1 scope; the codebase now contains additional packages and features beyond that scope.

### Key Findings

| Category | Count |
|----------|-------|
| New packages missing from docs | 6 |
| New features in existing packages undocumented | 6 |
| Phantom/deprecated features to remove | 0 |
| Architecture changes requiring HLD update | 2 |

---

## 1. Ground Truth: Actual Package Layout

### engine/cmd/ (as-built)
```
cmd/
  engine/    # ✓ documented
  worker/    # ✗ NOT in lld.md — new cmd for dynamic worker mode
```

### engine/internal/ (as-built)
```
internal/
  auth/      # ✓ documented (Slice C)
  config/    # ✓ documented (Slice D)
  connect/   # ✓ documented (Slice B)
  decision/  # ✓ documented (Slice C)
  envfile/   # ✗ NOT in lld.md — new utility package
  flow/      # ✓ documented (Slice A)
    saga/    # ✗ NOT in lld.md — new sub-package (R3 saga/compensation)
  gateway/   # ✗ NOT in lld.md — new gateway package (dispatcher, scaler, queue, registry, manifests)
  httpapi/   # ✓ documented (Slice E)
  observ/    # ✓ documented (Slice E)
  scheduler/ # ✗ NOT in lld.md — new scheduler package (cron/scheduled task execution)
  webhook/   # ✗ NOT in lld.md — new webhook package (providers: GitHub, Stripe, generic)
  worker/    # ✗ NOT in lld.md — new worker package (group-isolated worker runtime)
```

---

## 2. Feature Verification Matrix

### 2.1 New Packages (VERIFIED-PRESENT, need documentation)

| Package | Existence | Evidence | Description |
|---------|-----------|----------|-------------|
| `internal/scheduler` | **VERIFIED-PRESENT** | `engine/internal/scheduler/scheduler.go` — full Scheduler struct with Start/Stop/loop methods, cron parsing, distributed locking | Background service for time-based flow triggers |
| `internal/worker` | **VERIFIED-PRESENT** | `engine/internal/worker/worker.go` — Worker struct with LoadGroup/Execute/Ready/Close, `engine/cmd/worker/` entry point | Group-isolated worker runtime for multi-tenant deployments |
| `internal/webhook` | **VERIFIED-PRESENT** | `engine/internal/webhook/` — provider.go, stripe.go, github.go, generic.go, filter.go, mapping.go | Webhook providers with signature verification, event filtering, payload mapping |
| `internal/gateway` | **VERIFIED-PRESENT** | `engine/internal/gateway/` — dispatcher.go, scaler.go, queue.go, registry.go, manifests.go, metrics.go, tracing.go | API gateway pattern: request dispatching to workers, auto-scaling, request queuing |
| `internal/envfile` | **VERIFIED-PRESENT** | `engine/internal/envfile/` — dependency-free `.env` loader | Utility for loading environment variables from .env files |
| `internal/flow/saga` | **VERIFIED-PRESENT** | `engine/internal/flow/saga/saga.go` — Context struct with State, Steps, Record, Commit, Compensate methods | Saga/compensation coordinator for cross-source write orchestration (R3 mitigation) |

### 2.2 New Features in Existing Packages (VERIFIED-PRESENT, need documentation)

| Feature | Package | Evidence | Description |
|---------|---------|----------|-------------|
| Filter/Find nodes | `internal/flow` | `control_handlers.go:409-532` — filterHandler and findHandler types with Exec methods; `node.go` has `TypeFilter = "filter"` and `TypeFind = "find"` constants | Collection nodes for in-engine filtering/finding using ZEN predicates |
| Map/Reduce nodes | `internal/flow` | `control_handlers.go:573-637` — mapHandler and reduceHandler types | Collection transformation nodes using ZEN predicates |
| Rate limiter (R6) | `internal/httpapi` | `ratelimit.go` — rateLimiter struct, newRateLimiter(), rateLimitMiddleware(), token bucket with RPS/Burst config | Token-bucket rate limiting middleware |
| Idempotency key (R4) | `internal/connect` | `dedup.go` — DedupStore interface, dedupGuard, guard() with SET-NX lock in valkey | Idempotency key support for non-idempotent writes |
| ActionSpec.IdempotencyKeyFrom | `internal/flow` | `spec.go:ActionSpec` field | Path resolver for idempotency keys from context |
| ActionSpec.UnwrapSingleRow | `internal/flow` | `spec.go:ActionSpec` field | De-demo adapter for single-row unwrapping |
| ConditionSpec.BranchField | `internal/flow` | `spec.go:ConditionSpec` field | De-demo adapter for explicit branch field naming |
| TraceNode integration | `internal/flow` | `interpreter.go` integrated TraceNode | Per-node trace recording in dry-run mode |

---

## 3. Per-Document Audit & Update Plan

### 3.1 lld.md — Package Layout & Composition

**Current state:** Documents original v1 scope. Missing 6 packages and 1 sub-package.

**Required edits:**

1. **§1 Package layout** — Update the tree to include:
   ```diff
   nzr-rules-engine/
     cmd/engine/            main: wire deps, start server, graceful shutdown (Slice E)
   + cmd/worker/            worker binary: group-isolated runtime for multi-tenant deployments
     internal/
       flow/                Slice A — node specs, interpreter, Ctx accumulator, validation, budgets
   +     saga/              saga/compensation coordinator for cross-source writes (R3)
       connect/             Slice B — Registry, resilientClient, resilience, secret
         drivers/           postgres (pgx), valkey, rest/http (net/http)
       decision/            Slice C — ZEN embed (CGO), compiled-JDM cache, Evaluator
       auth/                Slice C — AuthN (JWT/JWKS), AuthZ (ZEN decision)
       config/              Slice D — Store, versioning, cache, admin validate/dry-run
       observ/              Slice E — Tracer/Span/Logger, per-node trace, metrics, trace collector
       httpapi/             router + middleware chain + ops endpoints (Slice E owns ops)
   +   scheduler/           Slice F — background scheduler for time-based flow execution
   +   worker/              Slice G — group-isolated worker runtime (LoadGroup, Execute, hot-reload)
   +   webhook/             Slice H — webhook receiver (providers: GitHub, Stripe, generic)
   +   gateway/             Slice I — API gateway (dispatcher, scaler, queue, worker registry)
   +   envfile/             utility — dependency-free .env loader
     migrations/            Slice D — SQL migrations (golang-migrate)
   ```

2. **§2 Slice composition diagram** — Add gateway→worker dispatch flow:
   - Add nodes for gateway (dispatcher), worker, scheduler
   - Show gateway→worker dispatch path
   - Show scheduler→flow trigger path
   - Show webhook→flow trigger path

3. **§6 AC coverage map** — Add rows for new features:
   - R3 (saga/compensation): now implemented
   - R4 (idempotency): now implemented  
   - R6 (rate limiting): now implemented
   - Filter/Find/Map/Reduce nodes: new ACs needed

4. **§7 Build order** — Document the extended architecture phases

**Files to modify:** `docs/architecture/lld.md`
**Verify:** `go build ./...` passes; grep confirms new packages exist

---

### 3.2 lld-contracts.md — Shared Interfaces

**Current state:** Documents original frozen seams. Some new seams exist in the newer packages.

**Required edits:**

1. **§ Package layout** — Update to match §3.1 above

2. **§ Core shared types** — Add new type definitions:
   ```go
   // FilterSpec — filter collection node spec
   type FilterSpec struct {
       Over     string   `json:"over"`      // source array path
       JDMID    string   `json:"jdmId"`     // predicate decision
       Input    []string `json:"input"`     // fields to project to ZEN
       SaveAs   string   `json:"saveAs"`    // result key
       MaxItems int      `json:"maxItems"`  // budget guard (AC-17)
   }
   
   // FindSpec — find-first collection node spec
   type FindSpec struct {
       Over     string   `json:"over"`
       JDMID    string   `json:"jdmId"`
       Input    []string `json:"input"`
       SaveAs   string   `json:"saveAs"`
       MaxItems int      `json:"maxItems"`
   }
   
   // saga.Context — saga/compensation coordinator
   type Context struct { /* ... */ }
   ```

3. **§ ActionSpec** — Document new fields:
   ```go
   type ActionSpec struct {
       // ... existing fields ...
       IdempotencyKeyFrom string `json:"idempotencyKeyFrom,omitempty"` // R4: path to key
       UnwrapSingleRow    bool   `json:"unwrapSingleRow,omitempty"`    // de-demo adapter
   }
   ```

4. **§ ConditionSpec** — Document new fields:
   ```go
   type ConditionSpec struct {
       // ... existing fields ...
       BranchField string `json:"branchField,omitempty"` // de-demo: explicit branch field
   }
   ```

5. **New seams** — Document scheduler, worker, webhook, gateway interfaces (or reference new slice docs)

**Files to modify:** `docs/architecture/lld-contracts.md`
**Verify:** Types match actual Go definitions in spec.go

---

### 3.3 hld.md — High-Level Design

**Current state:** Documents v1 scope, risks R0-R14 with R3/R4/R6 as "deferred."

**Required edits:**

1. **§6 Risk register** — Update status:
   ```diff
   - | R3 | Multi-source writes not atomic | sagas/compensation next-phase | documented constraint |
   + | R3 | Multi-source writes not atomic | **MITIGATED: saga/compensation coordinator implemented** | implemented |
   
   - | R4 | Retried writes duplicate | v1 for required-writes; else phase 2 | |
   + | R4 | Retried writes duplicate | **IMPLEMENTED: IdempotencyKeyFrom + dedupGuard** | implemented |
   
   - | R6 | No rate limiting | phase 2 (document gap) | |
   + | R6 | No rate limiting | **IMPLEMENTED: token-bucket rate limiter** | implemented |
   ```

2. **§3 Core components** — Add new component descriptions:
   - Scheduler — background service for time-based flow triggers
   - Worker — group-isolated runtime for multi-tenant deployments
   - Webhook receiver — external event ingestion (GitHub, Stripe, generic)
   - Gateway — request dispatcher with auto-scaling

3. **§3 Node taxonomy** — Add collection nodes:
   ```diff
   Trigger · Action (I/O) · Control (...) · Decision (ZEN) · Set/Transform ·
   - Logger/Debug · Response.
   + Logger/Debug · **Filter** · **Find** · **Map** · **Reduce** · Response.
   ```

4. **§9 v1 scope** — Update "documented constraints" section since R3/R4/R6 are now resolved

5. **§10 AC** — Add ACs for new features:
   - AC-29: Filter node processes collection with ZEN predicate, respects maxItems
   - AC-30: Find node returns first matching item
   - AC-31: Saga coordinator runs compensations in reverse order on failure
   - AC-32: Rate limiter returns 429 with Retry-After when exceeded
   - AC-33: Scheduler executes flows on cron schedule
   - AC-34: Webhook receiver verifies signatures and triggers flows

**Files to modify:** `docs/architecture/hld.md`
**Verify:** Risk register matches STATE.md status

---

### 3.4 lld/slice-a-interpreter.md — Flow Interpreter

**Current state:** Documents original node types. Missing filter/find/map/reduce.

**Required edits:**

1. **§2 Node Spec definitions** — Add:
   ```go
   // 2.10 Filter — filter collection via ZEN predicate (leaf)
   type FilterSpec struct {
       Over     string   `json:"over"`      // path to source array
       JDMID    string   `json:"jdmId"`     // predicate JDM
       Input    []string `json:"input"`     // fields to project
       SaveAs   string   `json:"saveAs"`    // result destination
       MaxItems int      `json:"maxItems"`  // AC-17 budget guard
   }
   
   // 2.11 Find — find first matching item (leaf)
   type FindSpec struct { /* same fields as FilterSpec */ }
   
   // 2.12 Map — transform each item (leaf)
   type MapSpec struct { /* similar fields */ }
   
   // 2.13 Reduce — aggregate collection (leaf)
   type ReduceSpec struct {
       Over        string   `json:"over"`
       JDMID       string   `json:"jdmId"`
       Input       []string `json:"input"`
       SaveAs      string   `json:"saveAs"`
       MaxItems    int      `json:"maxItems"`
       InitialAcc  any      `json:"initialAcc"` // starting accumulator
   }
   ```

2. **§3 Handlers** — Document filterHandler, findHandler, mapHandler, reduceHandler

3. **§ saga sub-package** — Add new section:
   ```markdown
   ## 2.x Sub-package `internal/flow/saga`
   
   The saga package implements the orchestration-style saga/compensation coordinator
   for cross-source write orchestration (HLD R3 mitigation).
   
   - State machine: Started → Executing → Committed OR Compensating → Compensated/Failed
   - Record(): registers completed write with optional compensation spec
   - Commit(): marks saga successful
   - Compensate(): runs compensations in reverse order on failure
   ```

4. **ActionSpec additions** — Document IdempotencyKeyFrom, UnwrapSingleRow

5. **ConditionSpec additions** — Document BranchField

**Files to modify:** `docs/architecture/lld/slice-a-interpreter.md`
**Verify:** Grep confirms spec structs match control_handlers.go and spec.go

---

### 3.5 lld/slice-b-connections.md — Connections

**Current state:** Documents resilience but not idempotency dedup mechanism.

**Required edits:**

1. **§ Idempotency dedup** — Add section:
   ```markdown
   ## 6.2 Idempotency Key Dedup Guard
   
   The dedupGuard implements first-writer-wins semantics for non-idempotent writes (R4):
   
   - DedupStore interface: Acquire(key, ttl) → (acquired, error), Release(key)
   - Guard semantics: first writer acquires lock and runs write; subsequent writers
     get deduped response
   - Lock key format: `nzr:dedup:{connKey}:{opKind}:{idempotencyKey}`
   - TTL-bounded: crashed process cannot permanently suppress legitimate writes
   ```

2. **§ Operation** — Document IdempotencyKey field

3. **§ registryV2** — Document lazy-pool implementation (USE_CONNECTOR_POOL flag)

**Files to modify:** `docs/architecture/lld/slice-b-connections.md`
**Verify:** Grep confirms dedup.go exports match doc

---

### 3.6 lld/slice-f-admin-api.md — Admin API

**Current state:** Documents admin endpoints. May need webhook endpoints.

**Required edits:**

1. **Verify webhook admin endpoints are documented** (if any)

2. **Verify schedule admin endpoints are documented** (if any)

**Files to modify:** `docs/architecture/lld/slice-f-admin-api.md`
**Verify:** Compare against actual admin_handlers.go routes

---

### 3.7 NEW: lld/slice-g-scheduler.md (to create)

**Purpose:** Document the `internal/scheduler` package.

**Content outline:**
- Scheduler struct and Config
- DistributedLocker interface (prevents duplicate runs across instances)
- FlowExecutor and FlowResolver interfaces
- Cron parsing (ParseSchedule, NextRun, ValidateInterval)
- Schedule config store integration
- AC coverage

**Files to create:** `docs/architecture/lld/slice-g-scheduler.md`
**Verify:** Package exists at engine/internal/scheduler/

---

### 3.8 NEW: lld/slice-h-webhook.md (to create)

**Purpose:** Document the `internal/webhook` package.

**Content outline:**
- Provider interface (GitHub, Stripe, generic)
- Signature verification per provider
- Filter matching (MatchesFilter, MatchesFilterFromMap)
- Payload mapping (MapPayload)
- Webhook config and logging
- AC coverage

**Files to create:** `docs/architecture/lld/slice-h-webhook.md`
**Verify:** Package exists at engine/internal/webhook/

---

### 3.9 NEW: lld/slice-i-gateway.md (to create)

**Purpose:** Document the `internal/gateway` package.

**Content outline:**
- Dispatcher (request routing to workers)
- Scaler (auto-scaling worker instances)
- Queue (request queuing)
- Registry (worker registration and health)
- Manifests (Kubernetes manifest generation)
- Metrics and Tracing
- AC coverage

**Files to create:** `docs/architecture/lld/slice-i-gateway.md`
**Verify:** Package exists at engine/internal/gateway/

---

### 3.10 NEW: lld/slice-j-worker.md (to create)

**Purpose:** Document the `internal/worker` and `cmd/worker` packages.

**Content outline:**
- Worker struct (group-isolated runtime)
- LoadGroup (config loading and hot-reload)
- Execute (flow execution)
- Handler (HTTP endpoints: /execute, /healthz, /readyz, /debug/config)
- WorkerStore interface
- AC coverage

**Files to create:** `docs/architecture/lld/slice-j-worker.md`
**Verify:** Package exists at engine/internal/worker/

---

### 3.11 docs/operations/ — Operational Docs

**Current state:** Contains DEPLOYMENT.md, DOCKER.md, MONITORING.md, SECRETS.md, runbook-dynamic-workers.md

**Required edits:**

1. **runbook-dynamic-workers.md** — Verify it aligns with actual gateway/worker architecture

2. **MONITORING.md** — Verify metrics names match actual metrics in gateway/metrics.go

**Files to review:** All files in docs/operations/
**Verify:** Metric names and runbook procedures match actual code

---

### 3.12 docs/examples/ — Example Docs

**Current state:** Contains examples-fmc-order.md

**Required edits:**

1. Verify example flows use current node types and specs

**Files to review:** docs/examples/examples-fmc-order.md
**Verify:** Example JSON matches current spec.go definitions

---

## 4. Phantom/Deprecated Features Check

**Result:** No phantom features found. All features documented in architecture docs exist in code.

---

## 5. Summary: Update Priority

| Priority | Document | Action |
|----------|----------|--------|
| **HIGH** | lld.md | Update package layout to include 6 new packages |
| **HIGH** | hld.md | Update risk register (R3/R4/R6 now implemented) |
| **HIGH** | lld-contracts.md | Add new node specs and interface additions |
| **MEDIUM** | slice-a-interpreter.md | Document filter/find/map/reduce nodes, saga sub-package |
| **MEDIUM** | slice-b-connections.md | Document idempotency dedup guard |
| **MEDIUM** | Create slice-g-scheduler.md | New slice doc for scheduler |
| **MEDIUM** | Create slice-h-webhook.md | New slice doc for webhook |
| **MEDIUM** | Create slice-i-gateway.md | New slice doc for gateway |
| **MEDIUM** | Create slice-j-worker.md | New slice doc for worker |
| **LOW** | slice-f-admin-api.md | Verify coverage of new endpoints |
| **LOW** | operations/*.md | Verify alignment with current architecture |
| **LOW** | examples/*.md | Verify example specs are current |

---

## 6. Verification Commands

After updates, run these to confirm alignment:

```bash
# Verify all documented packages exist
ls engine/internal/{flow,connect,decision,auth,config,observ,httpapi,scheduler,worker,webhook,gateway,envfile}
ls engine/internal/flow/saga
ls engine/cmd/{engine,worker}

# Verify documented node types exist
grep -n "Type[A-Z]" engine/internal/flow/node.go

# Verify documented spec structs exist
grep -n "type.*Spec struct" engine/internal/flow/spec.go

# Verify saga package exists
ls engine/internal/flow/saga/

# Verify rate limiter exists
grep -n "rateLimiter" engine/internal/httpapi/ratelimit.go

# Verify dedup guard exists
grep -n "dedupGuard" engine/internal/connect/dedup.go

# Build to confirm no broken references
cd engine && CGO_ENABLED=1 go build ./...
```

---

## 7. Appendix: Evidence Trail

### Package existence verification (run 2026-10-07)

```
engine/internal/scheduler/    ✓ exists (scheduler.go, cron.go, cron_test.go)
engine/internal/worker/       ✓ exists (worker.go, handler.go, reload.go, types.go, tests)
engine/internal/webhook/      ✓ exists (provider.go, stripe.go, github.go, generic.go, filter.go, mapping.go)
engine/internal/gateway/      ✓ exists (dispatcher.go, scaler.go, queue.go, registry.go, manifests.go)
engine/internal/envfile/      ✓ exists
engine/internal/flow/saga/    ✓ exists (saga.go, saga_test.go)
engine/cmd/worker/            ✓ exists
```

### Node type constants (from node.go)

```go
TypeTrigger   = "trigger"
TypeAction    = "action"
TypeCondition = "condition"
TypeSwitch    = "switch"
TypeSequence  = "sequence"
TypeParallel  = "parallel"
TypeForEach   = "forEach"
TypeDecision  = "decision"
TypeSet       = "set"
TypeLogger    = "logger"
TypeResponse  = "response"
TypeFilter    = "filter"   // NEW - not in original docs
TypeFind      = "find"     // NEW - not in original docs
TypeMap       = "map"      // NEW - not in original docs
TypeReduce    = "reduce"   // NEW - not in original docs
```

### R3/R4/R6 implementation evidence

- **R3 (saga):** `engine/internal/flow/saga/saga.go` — full state machine with Compensate()
- **R4 (idempotency):** `engine/internal/connect/dedup.go` — dedupGuard with Acquire/Release
- **R6 (rate limit):** `engine/internal/httpapi/ratelimit.go` — token bucket with 429 response
