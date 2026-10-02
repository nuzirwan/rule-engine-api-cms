# Phase 0 De-Risking Spike — Implementation Plan

**Branch/worktree:** `/home/nuzirwan/project/rule-engine-api/.worktrees/phase0-spike` ·
Module `nzr-rules-engine` · go.mod floor `go 1.22` · THROWAWAY.
**Goal:** prove the two riskiest ADRs cheaply before any interpreter work — ADR-003/R9 (embed
`zen-go` via CGO, build + run) and ADR-001/R7 (ZEN evaluates the hardest real condition) — and
run one nested flow end to end against real Postgres + REST (AC-S3).

**Status at plan time:** the single biggest unknown is already resolved — AC-S1 **PASSED** during
exploration (see §2). The rest of this plan reproduces that proof inside the worktree and builds
AC-S2 + AC-S3 on top of it.

**Scope guard (do NOT build):** config store, cache, versioning pointers, auth (AuthN/AuthZ),
observability/OTel, admin endpoints, the full node taxonomy. Keep every file minimal and
throwaway. Only the node kinds AC-S3 needs: Trigger → Action(pg read) → Condition(ZEN) → two
branches each Action(REST) → Set → Response.

---

## 0. Confirmed facts from exploration (ground truth — do not re-litigate)

### 0.1 zen-go module path + version (CONFIRMED against upstream, not guessed)
- **Module path is versioned v2: `github.com/gorules/zen-go/v2`.** The brief named
  `github.com/gorules/zen-go`; the real current module is the `/v2` path. v2 changed the import
  path and the callback-loader signature. ([Go SDK docs](https://docs.gorules.io/developers/sdks/go),
  [zen-go repo](https://github.com/gorules/zen-go)). *Content rephrased for license compliance.*
- **Pinnable latest version: `v2.1.2`** (tags observed: v2.0.0, v2.0.1, v2.1.0, v2.1.1, v2.1.2).
  Pin exactly: `go get github.com/gorules/zen-go/v2@v2.1.2`.
- **Native lib ships vendored in the module — NO separate Rust build.** The module carries
  prebuilt static libs `deps/<os>_<arch>/libzen_ffi.a` (linux_amd64, linux_arm64, darwin_amd64,
  darwin_arm64, windows_amd64) plus `zen_engine.h`. There is nothing to compile from Rust; `go
  get` pulls the archive, cgo links `-lzen_ffi` from `${SRCDIR}/deps/...`. This materially
  de-risks R9's "native lib must be present at build time" worry — it is present, in-module.

### 0.2 The REAL zen-go API (verified by reading v2 source `zen.go`/`engine.go`/`decision.go`)
```go
import zen "github.com/gorules/zen-go/v2"

// Construct (no error return; init failure is deferred into the engine value):
eng := zen.NewEngine(zen.EngineConfig{})            // empty cfg = no loader; use CreateDecision
// Loaders (optional, for Evaluate-by-key): zen.StaticLoader{Content: map[string]json.RawMessage},
//   zen.FilesystemLoader{Path: "./rules"}, zen.ZipLoader{Bytes: ...}, or a zen.Loader callback.

// Compile a JDM from raw bytes -> a reusable Decision handle:
dec, err := eng.CreateDecision(jdmBytes)            // []byte in

// Evaluate (JSON in, JSON out):
resp, err := dec.Evaluate(input)                    // input is `any` (map/struct), JSON-marshalled
//   resp.Result is json.RawMessage (the JDM output object) — NOT a map[string]any.
//   resp.Trace is *json.RawMessage (populated only with EvaluateWithOpts{Trace:true}).

// Lifecycle is Dispose(), NOT Close():
dec.Dispose()
eng.Dispose()

// Engine also exposes Evaluate(key, ctx) / GetDecision(key) when a loader is configured,
// and EvaluateBatch(...). EvaluationOptions{Trace bool; MaxDepth uint8} (MaxDepth defaults to 1).
```

### 0.3 Where the real API DIVERGES from Slice C's assumptions (first-class findings — R7/ADR-001 impact)
Slice C (`docs/lld/slice-c-zen-auth.md`) assumed a `Compiled` handle shaped
`Eval(ctx, input) (map[string]any, error)` + `Close()`. The real binding differs — **the Slice C
design must be revised at stitch time**, but the divergences are adaptable, not blocking:

| Slice C assumption | Real zen-go v2 | Adaptation (what the engine's `decision` package must do) |
| --- | --- | --- |
| `Compiled.Eval(ctx, input)` | `Decision.Evaluate(input any)` — **no `context.Context` arg** | ZEN eval is a fast in-process CPU call; ctx cancellation/deadline cannot abort it mid-eval. Honor ctx **around** the call (check `ctx.Err()` before; optionally run in a goroutine with select for a hard timeout). Slice C's `Timeout` classification must reflect "no in-eval cancellation." |
| returns `map[string]any` | returns `*EvaluationResponse{Result json.RawMessage}` | `decision.Engine` unmarshals `resp.Result` into `map[string]any` at the seam, so the fixed `decision.Evaluator.Evaluate(ctx, jdmID, input) (map[string]any, error)` contract in `lld-contracts.md` is **preserved** — the adaptation is internal. |
| `Close()` | `Dispose()` | `Compiled` wrapper's `Close()` calls `Dispose()`. Engine-level shutdown calls `engine.Dispose()`. |
| compiled handle = `*zen.Decision` | `zen.Decision` (interface) from `CreateDecision([]byte)` | compiled-cache value type is `zen.Decision`; `CreateDecision` is the compile step to cache by `(id,version)`. Confirmed: compile-once/evaluate-many is the intended pattern. |
| module `github.com/gorules/zen-go` | `.../zen-go/v2` | pin v2 path everywhere. |

**Verdict vs ADR-001/ADR-003 and Slice C:** ADR-001 (ZEN as the pure JSON-in/JSON-out decision
engine) and ADR-003 (embed via CGO, in-process) **hold** — the API is a clean compile-then-eval
decision engine with no I/O. Slice C's *seam* (`decision.Evaluator`) survives unchanged; only its
*internal* `Compiled` wrapper shape (ctx handling, `Dispose` vs `Close`, raw-JSON unmarshal) needs
the adaptations above. These are stitch-time edits to Slice C, flagged here as spike findings.

### 0.4 CGO build + AC-S1 result OBSERVED during exploration (the proof already ran)
- `#cgo` flags come entirely from the binding's `cgo.go` (we add none): `LDFLAGS: -pthread
  -lzen_ffi` + per-OS/arch `-L${SRCDIR}/deps/...`; linux adds `-lm -ldl`. Requires a C toolchain
  (`gcc`/`cc` present; both confirmed on this host) and `CGO_ENABLED=1`.
- **AC-S1 build+run PASSED** with Go 1.26.2, `CGO_ENABLED=1`, the §3 JDM, on linux/amd64:
  ```
  input={amount:1500 status:paid}    => {"shipping":"expedited"}
  input={amount:500  status:paid}    => {"shipping":"standard"}
  input={amount:1500 status:pending} => {"shipping":"standard"}
  ```
- **R9 constraints characterized with evidence (document, do not fight):**
  - Default build is **dynamically linked** against `libm`, `libgcc_s`, `libc` (glibc) — `ldd`
    confirmed. The target image must carry a matching **glibc** runtime.
  - `CGO_ENABLED=0` **fails to compile** (`undefined: zen.NewEngine`) — proving the Slice C §2
    build-tag stub is mandatory for any pure-Go lint/vet lane.
  - Fully-static (`-extldflags "-static"`) **links with glibc warnings** (`getpwuid_r`,
    `getaddrinfo` need the matching glibc at runtime) and runs on the same host, but is **not
    portable to a different glibc or to musl/Alpine**. Target-image guidance: use a **glibc base**
    (debian-slim / distroless), NOT Alpine, unless the native lib is rebuilt for musl.
  - Cross-compile is constrained: only the arches with a vendored `deps/<os>_<arch>` lib are
    linkable, and each needs the matching cross C toolchain. amd64/arm64 linux + darwin + windows
    amd64 are shipped.

---

## 1. Spike layout to build (inside the worktree)

```
nzr-rules-engine/
  internal/zenspike/
    zen.go              # thin wrapper over zen-go v2 (build tag: cgo)
    zen_stub.go         # //go:build !cgo guard (mirrors Slice C §2)
    zen_test.go         # AC-S2 table-driven decision test (build tag: cgo)
    testdata/order.jdm.json   # the representative JDM (AC-S2 model)
  internal/spikeflow/
    interpreter.go      # minimal tree-walk: Trigger->Action->Condition->branches->Set->Response
    nodes.go            # the few node structs + handlers the spike needs
    interpreter_test.go # AC-S3 end-to-end test (build tag: integration,cgo)
  cmd/spike/main.go     # optional: the AC-S1 smoke main (same as the verified snippet)
```
All spike code is throwaway and lives under `internal/zenspike` + `internal/spikeflow` so it is
trivially deletable and never collides with the real v1 packages (`internal/decision`,
`internal/flow`).

---

## Implementation items (ordered by dependency; each leaves the tree buildable)

- [ ] 1. **Add the dependency, pinned.** Run `go get github.com/gorules/zen-go/v2@v2.1.2` in the
      worktree so `go.mod`/`go.sum` pin v2.1.2 and its transitive deps (`tidwall/gjson` etc.).
      Files: `go.mod`, `go.sum`.
      Verify: `go list -m github.com/gorules/zen-go/v2` prints `v2.1.2`; `git -C <worktree> diff
      go.mod` shows the require line.

- [ ] 2. **Write the representative JDM (AC-S2 model) as testdata.** A single decision-table JDM:
      inputs `amount` (expression) + `status` (expression), one output `shipping`; rule 1 =
      `amount > 1000` AND `status == 'paid'` → `'expedited'`, fallthrough rule → `'standard'`
      (hitPolicy `first`). Model it on the confirmed JDM graph schema (inputNode →
      decisionTableNode → outputNode, with `edges`) verified in §3 below.
      Files: `internal/zenspike/testdata/order.jdm.json`.
      Verify: `go run ./cmd/spike` (after item 3) prints the three §3 outcomes; or inspected in
      item 5's test.

- [ ] 3. **Write the zen wrapper + CGO stub + AC-S1 smoke main.** `zen.go` (`//go:build cgo`)
      wraps: `NewEngine(EngineConfig{})`, `CreateDecision(bytes)` → a `Compiled`-like handle with
      `Eval(input any) (map[string]any, error)` (unmarshals `resp.Result`) and `Close()` →
      `Dispose()`. `zen_stub.go` (`//go:build !cgo`) exposes the same symbols returning a clear
      "built without CGO; ZEN unavailable" error (mirrors Slice C §2). `cmd/spike/main.go` loads
      the item-2 JDM, evaluates the three cases, prints results — the exact proof that already
      passed in exploration.
      Files: `internal/zenspike/zen.go`, `internal/zenspike/zen_stub.go`, `cmd/spike/main.go`.
      Verify (**AC-S1**): `CGO_ENABLED=1 go build ./...` succeeds; `CGO_ENABLED=1 go run
      ./cmd/spike` prints `expedited / standard / standard` and `AC-S1 OK`. Also confirm
      `CGO_ENABLED=0 go vet ./internal/zenspike` compiles via the stub (no `zen` undefined errors).

- [ ] 4. **Record the R9 build evidence.** Capture `ldd` on the AC-S1 binary (dynamic glibc link),
      the `CGO_ENABLED=0` compile failure of the cgo build, and the static-link glibc warnings, into
      this plan's §0.4 (already captured) — re-confirm they reproduce in-worktree.
      Files: none (evidence into findings).
      Verify: `CGO_ENABLED=1 go build -o /tmp/spikebin ./cmd/spike && ldd /tmp/spikebin` lists
      `libc.so.6`; `CGO_ENABLED=0 go build ./cmd/spike` fails as expected.

- [ ] 5. **AC-S2 table-driven decision test.** `zen_test.go` (`//go:build cgo`) compiles the item-2
      JDM once via the wrapper and asserts the branch per input over a table:
      `{amount:1500,status:"paid"}→expedited`, `{500,"paid"}→standard`,
      `{1500,"pending"}→standard`, plus boundary `{1000,"paid"}→standard` (strict `>`) and
      `{1001,"paid"}→expedited`. Assert `resp.Result`/the mapped `shipping` field equals expected.
      Files: `internal/zenspike/zen_test.go`.
      Verify (**AC-S2**): `CGO_ENABLED=1 go test ./internal/zenspike` passes all table cases.

- [ ] 6. **Minimal interpreter for AC-S3 (Slice A shape, trimmed).** `nodes.go` + `interpreter.go`
      implement just enough: a `Node{Type, Spec, Children}` tree and a recursive `walk` threading a
      `Ctx{Input, Data, Response map[string]any}` with a tiny `SetPath` (dotted, leaf-only assign,
      no clobber — the AC-3 behavior) and `GetPath` (merged Response→Data→Input view). Node kinds:
      `trigger` (seeds Input), `action` (pluggable func: pg-read or rest-call), `condition` (calls
      the item-3 ZEN wrapper, picks trueKey/falseKey), `set` (SetPath), `response` (stops). Keep
      handlers as small structs mirroring `docs/lld/slice-a-interpreter.md` §3 but without budget,
      parallel, forEach, validation, tracing. Decision driven by the real
      `decision.Evaluator`-shaped seam from item 3.
      Files: `internal/spikeflow/nodes.go`, `internal/spikeflow/interpreter.go`.
      Verify: `CGO_ENABLED=1 go build ./...` succeeds; a pure in-memory unit test (no Docker) walks
      a tiny tree with stub actions and asserts the stitched `Response` — run
      `CGO_ENABLED=1 go test ./internal/spikeflow -run TestWalkInMemory`.

- [ ] 7. **AC-S3 end-to-end: Postgres (ephemeral Docker) + REST (httptest) nested flow.**
      `interpreter_test.go` (`//go:build integration && cgo`) builds the nested flow:
      `Trigger → Action(pg read: SELECT amount,status FROM orders WHERE id=$1) → Condition(ZEN eval
      over {amount,status}) → { trueKey: Action(REST POST /expedite) ; falseKey: Action(REST POST
      /standard) } → Set(response.shipping, result) → Response`. This is ≥2 nesting depth
      (Sequence/Trigger → Condition → branch Action). Back the pg read with an **ephemeral Docker
      Postgres** started by the test (`docker run -d -e POSTGRES_PASSWORD=... -p 0:5432 postgres:16`
      or `ory/dockertest` / `testcontainers-go`), seeded with ONE row via `database/sql`+`pgx`
      stdlib driver (or `pgx` directly); back the REST actions with an in-process
      `httptest.Server` returning canned JSON and recording which path was hit. Assert the final
      stitched response body AND that the correct REST endpoint was called for a paid-high-value
      order vs. a standard one (two sub-cases).
      Files: `internal/spikeflow/interpreter_test.go`; add `github.com/jackc/pgx/v5` (and
      optionally `github.com/ory/dockertest/v3` or `github.com/testcontainers/testcontainers-go`)
      pinned via `go get`.
      Verify (**AC-S3**): `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/spikeflow`
      passes (Docker daemon is UP — confirmed). Expedited sub-case hits `/expedite` and returns
      `{"shipping":"expedited"}`; standard sub-case hits `/standard`.

- [ ] 8. **Full spike verification pass + findings write-up.** Run the whole suite the way CI
      would: the CGO unit lane and the integration lane; record outcomes and any surprises
      (especially R9: libc base-image guidance, no-musl/Alpine note, static-link warnings) into the
      spike findings / `review.json` consumed by the loop.
      Files: none (verification + findings).
      Verify: `CGO_ENABLED=1 go test ./...` (unit) passes; `CGO_ENABLED=1 go test -tags 'integration
      cgo' ./...` passes; `CGO_ENABLED=0 go vet ./internal/zenspike` compiles via stub.

---

## 2. Build-tag matrix (which tests run where)

| Code | Build tags | CGO | Needs Docker | Proves |
| --- | --- | --- | --- | --- |
| `internal/zenspike` wrapper + `cmd/spike` | `cgo` (real) / `!cgo` (stub) | 1 for real | no | AC-S1 |
| `internal/zenspike/zen_test.go` | `//go:build cgo` | 1 | no | AC-S2 |
| `internal/spikeflow` interpreter + in-mem test | (none; builds under cgo) | 1 | no | interpreter shape |
| `internal/spikeflow/interpreter_test.go` | `//go:build integration && cgo` | 1 | **yes** (ephemeral PG) | AC-S3 |

Rationale: the Docker integration test is **build-tagged `integration`** so the default `go test
./...` lane stays Docker-free and fast; the CGO lane is the real binding, the `!cgo` stub keeps a
pure-Go vet/lint lane compiling (Slice C §2). This matches the Slice C/§9 "stub lane + CGO lane"
split.

---

## 3. The representative JDM graph (AC-S2) — verified schema

Model: given an order `{amount, status}`, `amount > 1000 AND status == 'paid'` → `expedited`, else
`standard`. JDM is a graph of `nodes` (inputNode → decisionTableNode → outputNode) + `edges`, matching
the upstream decision-table schema confirmed during exploration:

```json
{
  "nodes": [
    {"id":"in","type":"inputNode","name":"Request","position":{"x":100,"y":100}},
    {"id":"dt","type":"decisionTableNode","name":"Shipping","position":{"x":350,"y":100},
     "content":{
       "hitPolicy":"first",
       "inputs":[
         {"id":"i1","name":"Amount","field":"amount","type":"expression"},
         {"id":"i2","name":"Status","field":"status","type":"expression"}
       ],
       "outputs":[{"id":"o1","name":"Shipping","field":"shipping","type":"expression"}],
       "rules":[
         {"_id":"r1","i1":"> 1000","i2":"== 'paid'","o1":"'expedited'"},
         {"_id":"r2","i1":"","i2":"","o1":"'standard'"}
       ]
     }},
    {"id":"out","type":"outputNode","name":"Response","position":{"x":600,"y":100}}
  ],
  "edges":[
    {"id":"e1","type":"edge","sourceId":"in","targetId":"dt"},
    {"id":"e2","type":"edge","sourceId":"dt","targetId":"out"}
  ]
}
```

Table cases (AC-S2): `{1500,"paid"}→expedited`, `{500,"paid"}→standard`,
`{1500,"pending"}→standard`, `{1000,"paid"}→standard` (strict `>`), `{1001,"paid"}→expedited`.
*(The first three were already observed passing in exploration.)*

---

## 4. Exact commands reference (as observed / to reproduce)

```bash
WT=/home/nuzirwan/project/rule-engine-api/.worktrees/phase0-spike
cd "$WT"
# 1. dependency (pinned)
go get github.com/gorules/zen-go/v2@v2.1.2
# 3. AC-S1 build + run  (OBSERVED PASS in exploration)
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go run ./cmd/spike        # => expedited / standard / standard + "AC-S1 OK"
# 4. R9 evidence
CGO_ENABLED=1 go build -o /tmp/spikebin ./cmd/spike && ldd /tmp/spikebin   # libc.so.6 etc. (dynamic/glibc)
CGO_ENABLED=0 go build ./cmd/spike      # FAILS: undefined zen.NewEngine (stub lane needed)
# 5. AC-S2
CGO_ENABLED=1 go test ./internal/zenspike
# 7. AC-S3 (Docker daemon is UP)
CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/spikeflow
# 8. CI-shaped full pass
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go test -tags 'integration cgo' ./...
CGO_ENABLED=0 go vet ./internal/zenspike   # stub compiles
```

---

## 5. Spike gate decision inputs (feeds the loop's review.json → verdict APPROVED)

- **AC-S1** (CGO build + run): **already PASSED** in exploration; item 3 reproduces it in-worktree.
- **AC-S2** (hardest condition as JDM, table-correct): item 5.
- **AC-S3** (one nested flow ≥2 depth, real PG + real REST, seeded, synchronous, correct stitched
  response): item 7.
- **R9 findings** (CGO tax): native lib vendored in-module (no Rust build); dynamic glibc link;
  Alpine/musl NOT supported without a musl rebuild → target image must be glibc-based
  (debian-slim/distroless); static linking warns and is non-portable; `CGO_ENABLED=0` must use the
  stub. All documented, none blocking.
- **ADR-001/ADR-003 verdict:** HOLD. **Slice C stitch edits required** (not blockers): adapt the
  `Compiled` wrapper to `Decision.Evaluate(input any)` (no ctx arg — honor ctx around the call),
  `*EvaluationResponse{Result json.RawMessage}` (unmarshal at the seam, keep `Evaluator` contract),
  `Dispose()` not `Close()`, module path `.../v2`. The fixed `decision.Evaluator` seam is unchanged.

**Open assumptions (reasonable, called out):** the integration test provisions its own ephemeral
Postgres (dockertest/testcontainers or raw `docker run`) — if the loop prefers one helper, use
`ory/dockertest` for the smallest footprint. If a future target image must be Alpine, that is a
real R9 action item (rebuild `libzen_ffi` for musl or switch to the sidecar fallback), not an AC
failure.
