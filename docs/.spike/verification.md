# Phase 0 Spike — Verification Evidence

Captured by the implementer so the reviewer can read the evidence without re-running
the suites. All commands run inside the worktree
`/home/nuzirwan/project/rule-engine-api/.worktrees/phase0-spike`.

**Environment:** Go 1.26.2 linux/amd64 · `cc (Ubuntu 11.4.0)` (glibc) · `CGO_ENABLED=1`
· Docker daemon UP · module `nzr-rules-engine` (go.mod floor `go 1.22`).

**Pinned deps:** `github.com/gorules/zen-go/v2 v2.1.2` (exact, as the plan verified) ·
`github.com/jackc/pgx/v5 v5.6.0`.

---

## Command log (exact commands + exact results)

### Dependency pin (AC-S1 setup)
```
$ go get github.com/gorules/zen-go/v2@v2.1.2
go: added github.com/gorules/zen-go/v2 v2.1.2
go: added github.com/tidwall/gjson v1.17.1
go: added github.com/tidwall/match v1.1.1
go: added github.com/tidwall/pretty v1.2.0
=> EXIT 0 ; go list -m github.com/gorules/zen-go/v2 => "v2.1.2"
```

### AC-S1 — CGO build + run
```
$ CGO_ENABLED=1 go build ./...
=> EXIT 0

$ CGO_ENABLED=1 go run ./cmd/spike
input=map[amount:1500 status:paid] => map[shipping:expedited]
input=map[amount:500 status:paid] => map[shipping:standard]
input=map[amount:1500 status:pending] => map[shipping:standard]
AC-S1 OK
=> EXIT 0
```
**Result: PASS.** The zen-go v2 CGO binding compiled and ran; the engine constructed,
compiled the JDM, and evaluated all three canonical cases correctly.

### AC-S1 — R9 CGO-tax evidence (build/link characterization)
```
$ CGO_ENABLED=1 go build -o /tmp/spikebin ./cmd/spike && ldd /tmp/spikebin
	linux-vdso.so.1
	libm.so.6   => /lib/x86_64-linux-gnu/libm.so.6
	libgcc_s.so.1 => /lib/x86_64-linux-gnu/libgcc_s.so.1
	libc.so.6   => /lib/x86_64-linux-gnu/libc.so.6
	/lib64/ld-linux-x86-64.so.2
=> dynamic link against glibc (libc/libm/libgcc_s). Target image MUST carry glibc.

$ CGO_ENABLED=1 go build -ldflags '-extldflags "-static"' -o /tmp/spikebin_static ./cmd/spike
/usr/bin/ld: ... warning: Using 'getpwuid_r' in statically linked applications requires
  at runtime the shared libraries from the glibc version used for linking
/usr/bin/ld: ... warning: Using 'getaddrinfo' in statically linked applications requires
  at runtime the shared libraries from the glibc version used for linking
=> EXIT 0 (links WITH glibc warnings). Static binary runs on THIS host but is NOT
   portable to a different glibc or to musl/Alpine. (`file` => "statically linked".)

$ CGO_ENABLED=0 go build -o /tmp/spikebin_nocgo ./cmd/spike
=> EXIT 0  (compiles via the //go:build !cgo stub — pure-Go, "not a dynamic executable")

$ /tmp/spikebin_nocgo
AC-S1 FAILED: zenspike: built without CGO; ZEN unavailable (CGO_ENABLED=1 required)
=> EXIT 1  (stub REFUSES at runtime with a clear error; never silently no-ops)

$ CGO_ENABLED=0 go vet ./internal/zenspike
=> EXIT 0  (stub lane compiles clean — Slice C §2 pure-Go lint/vet lane holds)
```

> **Divergence from plan §0.4 (documented, not a regression):** the plan recorded (from
> pre-stub exploration) that `CGO_ENABLED=0 go build ./cmd/spike` *fails* with
> `undefined: zen.NewEngine`. Once the `zen_stub.go` guard exists (as Slice C §2 mandates),
> the non-CGO build **compiles** and the binary **refuses at runtime** — which is the
> intended, better behavior. The important R9 property still holds: ZEN cannot be used
> without CGO; it just fails loudly instead of failing to link.

### AC-S2 — ZEN evaluates the real condition (table + determinism)
```
$ CGO_ENABLED=1 go test -v ./internal/zenspike
=== RUN   TestDecisionTable
--- PASS: TestDecisionTable (0.00s)
    --- PASS: .../high_paid_->_expedited
    --- PASS: .../low_paid_->_standard
    --- PASS: .../high_pending_->_standard
    --- PASS: .../boundary_exactly_1000_paid_->_standard_(strict_>)
    --- PASS: .../boundary_1001_paid_->_expedited
=== RUN   TestDeterminism
--- PASS: TestDeterminism (0.00s)
PASS
ok  	nzr-rules-engine/internal/zenspike	0.014s
=> EXIT 0
```
**Result: PASS.** The JDM models `amount > 1000 AND status == 'paid' => expedited, else
standard`, correct across all five table cases including the strict-`>` boundary
(1000 => standard, 1001 => expedited). Determinism: 50 repeats over two independently
compiled decisions all byte-identical.

### AC-S3 — one nested flow end-to-end (real Postgres + real REST + real ZEN)
```
$ CGO_ENABLED=1 go test -tags 'integration cgo' -v -run TestNestedFlowEndToEnd ./internal/spikeflow
=== RUN   TestNestedFlowEndToEnd
=== RUN   TestNestedFlowEndToEnd/paid_high-value_order_->_expedited
=== RUN   TestNestedFlowEndToEnd/paid_low-value_order_->_standard
--- PASS: TestNestedFlowEndToEnd (8.34s)
    --- PASS: .../paid_high-value_order_->_expedited (0.01s)
    --- PASS: .../paid_low-value_order_->_standard (0.00s)
PASS
ok  	nzr-rules-engine/internal/spikeflow	8.353s
=> EXIT 0
```
**Result: PASS.** Flow `Trigger -> Action(pg read) -> Condition(ZEN) -> {expedite|standard
Action(REST)} -> Set(shipping) -> Response` ran synchronously end to end against a REAL
ephemeral `postgres:16` Docker container (seeded via pgx: order 1 = {1500,paid}, order 2 =
{500,paid}) and a REAL in-process `httptest.Server`. Both branches verified:
order 1 => stitched `{"shipping":"expedited"}` and exactly `/expedite` hit; order 2 =>
`{"shipping":"standard"}` and exactly `/standard` hit. Nesting depth ≥ 2
(Trigger → Action → Condition → branch Action → Set → Response).

> Postgres provisioning: raw `docker run -d -P postgres:16` with the mapped host port
> resolved via `docker inspect` and a readiness poll (`pgx.Connect` + `Ping`), torn down
> with `docker rm -f` in the test cleanup. No testcontainers/dockertest dependency needed.

### CI-shaped full pass
```
$ CGO_ENABLED=1 go build ./...                               => EXIT 0
$ CGO_ENABLED=1 go vet ./...                                 => EXIT 0
$ CGO_ENABLED=1 go test ./...                                => EXIT 0 (zenspike ok, spikeflow ok)
$ CGO_ENABLED=1 go test -tags 'integration cgo' ./...        => EXIT 0 (spikeflow 8.56s ok)
$ CGO_ENABLED=0 go vet ./internal/zenspike                   => EXIT 0 (stub compiles)
$ go mod tidy                                                => no changes (go.mod stable)
```

---

## zen-go v2 API vs Slice C's `Compiled`/`Evaluator` assumptions (verified by reading v2 source)

| Slice C assumed | Real zen-go v2.1.2 | Impact |
| --- | --- | --- |
| `Compiled.Eval(ctx, input)` | `Decision.Evaluate(input any)` — **no ctx arg** | Adaptable. Spike honors ctx AROUND the call (`ctx.Err()` before/after). ZEN eval is a fast synchronous CPU call that cannot be cancelled mid-eval — Slice C's `Timeout` classification must reflect this. |
| returns `map[string]any` | returns `*EvaluationResponse{Result json.RawMessage}` | Adaptable. Spike unmarshals `resp.Result` into `map[string]any` at the seam, so the fixed `decision.Evaluator` contract is preserved. |
| `Close()` | `Dispose()` | Trivial. Wrapper `Close()` calls `Dispose()`. |
| `*zen.Decision` from `CreateDecision` | `zen.Decision` (interface) from `eng.CreateDecision([]byte) (Decision, error)` | Compile-once/evaluate-many confirmed; cache value type is `zen.Decision`. |
| `NewEngine()` returns `(engine, error)` | `zen.NewEngine(EngineConfig{}) Engine` — **no error**; init error deferred into the value, surfaces on first `CreateDecision`/`Evaluate` | Minor. Error handling moves to first use. |
| module `github.com/gorules/zen-go` | `.../zen-go/v2` (versioned import path) | Pin the `/v2` path. |

**Native lib:** `deps/<os>_<arch>/libzen_ffi.a` ships vendored in the module (linux/darwin
amd64+arm64, windows amd64) with `zen_engine.h`. No Rust build step. The binding's own
`cgo.go` supplies all `#cgo LDFLAGS` (`-pthread -lzen_ffi`, linux adds `-lm -ldl`); we add
none.
