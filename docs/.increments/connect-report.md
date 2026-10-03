# Increment report — Slice B: valkey connector + full resilience envelope

Branch: `feat/connect-resilience` (worktree `.worktrees/connect`, off mainline)
Scope touched: `internal/connect/**` (+ `internal/connect/drivers/**`), `go.mod`/`go.sum`
(new deps), and this report. No other package was edited.

Builds on the thin slice (pooled `Registry` one `*resilientClient` per key AC-5, env
`SecretProvider`, field-level `mergePolicy` AC-7, `ConnError` taxonomy, Connector factory,
postgres + rest/http drivers). The thin slice shipped timeout-only resilience; this increment
adds the valkey connector and completes the resilience envelope.

## What was built

### valkey connector (`internal/connect/drivers/valkey.go`)
- Behind the SAME Connector factory (`drivers.All`), registered as type `valkey` — the
  Registry, interpreter, and flow schema are unchanged (AC-8).
- Client lib: `github.com/valkey-io/valkey-go v1.0.78` (official valkey client). One shared
  connection pool per key (AC-5); the client `Timeout` is left to the resilience context, not
  set on the driver, so it composes with retry/breaker (consistent with the rest driver).
- Operations per Slice B §2.2:
  - `get {key}` → value; a missing key maps to `NotFound` (not an empty-but-ok result).
  - `set {key,value,ttl?,nx?}` → `{ok}`; `SET`/`SET NX` is naturally idempotent.
  - `del {key|keys}` → `{deleted}`.
  - `ping` → liveness probe used by `HealthCheck`.
- Also satisfies `connect.DedupStore` (atomic `SET NX PX` + `DEL`) to back the idempotency lock.

### Full resilience envelope (`internal/connect/client.go`, `resilience.go`, `dedup.go`)
- Composition order OUTER→INNER: **timeout → breaker → retry → inner** (Slice B §4.1). The
  timeout is outermost so retries cannot exceed the deadline; the breaker wraps retry so a
  tripped circuit short-circuits before any attempt and retry failures count toward tripping it;
  retry is innermost and is a no-op for non-idempotent / non-transient work.
- **Retry** (`retry-and-backoff`): exponential backoff with FULL jitter and a hard cap,
  cancellable sleep (respects ctx/shutdown). Runs ONLY for idempotent ops; replays ONLY
  transient classes (`Timeout`, `Upstream`). `Validation`/`NotFound` are poison and never
  retried. `ErrBreakerOpen` is surfaced as `Upstream` but never retried in-call (the breaker
  owns recovery via its half-open probe).
- **Circuit breaker** (`github.com/sony/gobreaker/v2 v2.4.0`): ONE breaker **per connection
  key**, built once at construction — **per-instance (R10)**. NOT re-created from a per-node
  override (breaker state is a property of the downstream, not the call site, §4.5). A ctx
  cancellation is excluded from breaker accounting; only transient downstream failures count.
- **Per-node override precedence (AC-7)**: unchanged field-level, node-wins `mergePolicy` is
  now applied for Timeout AND Retry per Execute; the breaker instance is deliberately not
  overridable per node (one per key regardless of overrides).
- **Idempotency-key mechanism (R4, §6.2)** (`dedup.go`): an opted-in non-idempotent write
  (`Operation.IdempotencyKey` set, kind not naturally idempotent) takes an atomic `SET NX`
  dedup lock. First writer wins; a subsequent writer holding the lock is deduped (skip); a
  failed guarded write RELEASES the lock so a legitimate retry re-attempts; the lock carries a
  TTL so a crash can't leave a permanent tombstone. Dedup-store failure posture is
  **pessimistic** — a lock-store error BLOCKS the write (surfaced as `Upstream`) rather than
  risking a duplicate (`idempotency-and-dedup`). The registry auto-wires the valkey connection
  as the shared dedup store when one is configured; `connect.New`'s signature is unchanged.

### Honest limitation (R10) — documented, not hidden
Breaker state lives **in the process**, per engine instance. With K replicas there are K
independent breakers for one source: a source failing for everyone trips each replica's breaker
separately, and the "open" view is per-replica, not global. v1 **accepts** this; a globally
shared breaker (state in valkey) is out of scope because it adds hot-path latency and a
shared-state dependency to every call. Surfaced here per AC-28 and in the code
(`newBreaker` / `resilience.go`).

### Dependency note (go.mod)
Adding `valkey-go` bumped the module's `go` directive 1.22 → 1.25.0 (valkey-go's floor). Deps are
pinned to exact versions (`sony/gobreaker/v2 v2.4.0`, `valkey-io/valkey-go v1.0.78`). The build
runs on the environment's Go 1.26. This go.mod change is inherent to the task's requirement to
add a valkey client lib; the orchestrator's integration join will reconcile go.mod/cmd/engine.

## AC → test map

| AC | What it asserts | Test |
|----|-----------------|------|
| **AC-5** | one pooled client per key, pointer identity, Open once | `TestClientPointerIdentity`, `TestReloadReconciles` (unit) |
| **AC-6** | breaker trips after threshold, fails fast WITHOUT inner call, half-open recovers, and a second connection's breaker stays closed (isolation) | `TestBreakerOpensAndIsolates` (unit, fault injection) |
| **AC-7** | per-node timeout override wins; field-level merge keeps base retry/breaker | `TestTimeoutOverrideWins`, `TestMergePolicyFieldLevel` (unit) |
| **AC-8** | new `valkey` type served by the UNCHANGED registry via the Connector factory | `TestValkeyConnectorIntegration` (integration) |
| **AC-20** | registry resolves `SecretRef` without leaking; `Secret` redacts | existing `secret.go` contract (`Secret.String()`/`MarshalJSON` → `***`); integration path resolves the ref via the env provider |
| retry correctness (supports AC-6/R4) | transient+idempotent retried; validation never; non-idempotent write at-most-once; idempotency key makes a write retryable | `TestRetryOnlyIdempotent` (unit) |
| idempotency dedup (R4) | lock released on failure then retry succeeds; second writer deduped; store error blocks (pessimistic); natural idempotent write skips the lock | `TestIdempotencyDedupLock` (unit); `TestValkeyConnectorIntegration` SET-NX round-trip (integration) |

## Verification output (real)

```
########## go build ##########
$ CGO_ENABLED=1 go build ./internal/connect/...
exit=0

########## go vet ##########
$ CGO_ENABLED=1 go vet ./internal/connect/...
exit=0

########## go test (unit) ##########
$ CGO_ENABLED=1 go test -count=1 -v ./internal/connect/
=== RUN   TestClientPointerIdentity
--- PASS: TestClientPointerIdentity (0.00s)
=== RUN   TestUnknownKeyIsValidation
--- PASS: TestUnknownKeyIsValidation (0.00s)
=== RUN   TestUnsupportedKindIsValidation
--- PASS: TestUnsupportedKindIsValidation (0.00s)
=== RUN   TestTimeoutOverrideWins
--- PASS: TestTimeoutOverrideWins (0.55s)
=== RUN   TestReloadReconciles
--- PASS: TestReloadReconciles (0.00s)
=== RUN   TestMergePolicyFieldLevel
--- PASS: TestMergePolicyFieldLevel (0.00s)
=== RUN   TestRetryOnlyIdempotent
    --- PASS: TestRetryOnlyIdempotent/idempotent_read_retries_up_to_budget (0.00s)
    --- PASS: TestRetryOnlyIdempotent/non-idempotent_write_is_at-most-once (0.00s)
    --- PASS: TestRetryOnlyIdempotent/validation_error_is_never_retried (0.00s)
    --- PASS: TestRetryOnlyIdempotent/idempotency_key_makes_a_write_retryable (0.00s)
--- PASS: TestRetryOnlyIdempotent (0.00s)
=== RUN   TestBreakerOpensAndIsolates
--- PASS: TestBreakerOpensAndIsolates (0.06s)
=== RUN   TestIdempotencyDedupLock
    --- PASS: TestIdempotencyDedupLock/lock_released_on_failure,_retry_re-attempts_and_succeeds (0.00s)
    --- PASS: TestIdempotencyDedupLock/second_writer_holding_the_lock_is_deduped_(skip) (0.00s)
    --- PASS: TestIdempotencyDedupLock/dedup_store_error_blocks_the_write_(pessimistic) (0.00s)
    --- PASS: TestIdempotencyDedupLock/natural_idempotent_write_does_not_take_the_lock (0.00s)
--- PASS: TestIdempotencyDedupLock (0.00s)
PASS
ok  	nzr-rules-engine/internal/connect	0.621s

########## go test (integration, ephemeral valkey) ##########
$ CGO_ENABLED=1 go test -count=1 -tags 'integration cgo' -run TestValkeyConnectorIntegration -v ./internal/connect/drivers/
=== RUN   TestValkeyConnectorIntegration
--- PASS: TestValkeyConnectorIntegration (1.09s)
PASS
ok  	nzr-rules-engine/internal/connect/drivers	1.096s
```

All commands exit 0; build clean, vet clean, unit + integration pass. Breakers kept per-instance
(R10) and documented. Left on branch `feat/connect-resilience` for the orchestrator's join.
