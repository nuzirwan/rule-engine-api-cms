# Phase 0 De-Risking Spike — GO / NO-GO Verdict

**Subject:** `nzr-rules-engine` — proving the two riskiest architectural bets before the
full v1 build.
**Worktree/branch:** `.worktrees/phase0-spike` on `spike/phase0` (THROWAWAY).
**Environment:** Go 1.26.2 linux/amd64 · gcc 11.4 (glibc) · `CGO_ENABLED=1` · Docker UP.
**Pinned:** `github.com/gorules/zen-go/v2 v2.1.2`, `github.com/jackc/pgx/v5 v5.6.0`.
**Full command evidence:** see [`.spike/verification.md`](./.spike/verification.md).

## Overall recommendation: **GO** — proceed to the full v1 build.

All three acceptance criteria PASS. ADR-001 (ZEN as the pure JSON-in/JSON-out decision
engine) and ADR-003 (embed ZEN in-process via CGO) **HOLD** against the real zen-go v2.1.2
API and build. The CGO tax flagged by HLD **R9** is real but **characterized and
manageable** (glibc base image, not Alpine; stub lane for non-CGO). Slice C needs small,
non-blocking **stitch-time edits** to match the real binding — none of them invalidate the
fixed `decision.Evaluator` seam. No faked PASS, no mocked ZEN, no faked Postgres.

---

## AC-S1 — CGO build of ZEN: **PASS**

**What was proven.** Added `zen-go/v2@v2.1.2` (pinned), constructed the engine, compiled a
JDM, and evaluated it — built and ran with `CGO_ENABLED=1`.

**Commands + result (exact):**
```
go get github.com/gorules/zen-go/v2@v2.1.2        => v2.1.2 pinned
CGO_ENABLED=1 go build ./...                      => EXIT 0
CGO_ENABLED=1 go run ./cmd/spike                  => expedited / standard / standard + "AC-S1 OK", EXIT 0
```

**CGO surprises (HLD R9 — documented honestly):**
- **No Rust build.** The native lib is vendored in-module as prebuilt static archives
  `deps/<os>_<arch>/libzen_ffi.a` + `zen_engine.h` (linux/darwin amd64+arm64, windows amd64).
  `go get` pulls it; cgo links `-lzen_ffi` from `${SRCDIR}/deps/...`. R9's "native lib must be
  present at build time" worry is materially reduced — it is present, in-module.
- **cgo flags come from the binding, not us.** `cgo.go` supplies `LDFLAGS: -pthread -lzen_ffi`
  (+ linux `-lm -ldl`, + per-OS/arch `-L${SRCDIR}/deps/...`). We add zero cgo flags.
- **Dynamic glibc link.** `ldd` on the default binary shows `libc.so.6`, `libm.so.6`,
  `libgcc_s.so.1`. **The target image must carry a matching glibc runtime.**
- **Alpine/musl is NOT supported out of the box.** Fully-static linking links *with glibc
  warnings* (`getpwuid_r`, `getaddrinfo` need the linking glibc at runtime) and is **not
  portable** to a different glibc or to musl. **Action for v1: base the image on glibc
  (debian-slim / distroless), not Alpine**, unless `libzen_ffi` is rebuilt for musl (an R9
  action item, not a blocker — the sidecar remains the documented fallback).
- **Non-CGO lane behaves correctly.** `CGO_ENABLED=0 go vet ./internal/zenspike` compiles via
  the `//go:build !cgo` stub, and the non-CGO binary **refuses at runtime** with
  `"built without CGO; ZEN unavailable"` — it never silently no-ops. (This refines plan §0.4,
  which recorded a pre-stub *link* failure; with the Slice C §2 stub in place the behavior is
  compile-then-refuse, which is the intended design.)

---

## AC-S2 — ZEN evaluates a real condition: **PASS**

**What was proven.** One representative decision-table JDM
(`internal/zenspike/testdata/order.jdm.json`) models the hardest real condition:
`amount > 1000 AND status == 'paid' => 'expedited', else 'standard'` (hitPolicy `first`).
A table-driven test asserts the branch per input and determinism.

**Commands + result (exact):**
```
CGO_ENABLED=1 go test ./internal/zenspike    => PASS (EXIT 0)
```
Table cases, all PASS: `{1500,paid}=>expedited`, `{500,paid}=>standard`,
`{1500,pending}=>standard`, `{1000,paid}=>standard` (strict `>`), `{1001,paid}=>expedited`.
Determinism: 50 repeats across two independently compiled decisions — byte-identical output
(AC-4 property). The decision is pure (no clock/random/I/O), so ADR-001's determinism
guarantee holds.

**Does the API match Slice C's `Compiled`/`Evaluator`?** Mostly — with adaptations (below).
The decision-table semantics and compile-once/evaluate-many pattern match exactly.

---

## AC-S3 — one nested flow end-to-end: **PASS**

**What was proven.** A minimal tree-walking interpreter (`internal/spikeflow`) runs
`Trigger -> Action(pg read) -> Condition(ZEN eval) -> {expedite|standard Action(REST)} ->
Set(shipping) -> Response` **synchronously**, end to end, against:
- a **REAL ephemeral `postgres:16`** Docker container (started by the test, seeded via pgx
  with order 1 = {1500,paid} and order 2 = {500,paid}, torn down on cleanup), and
- a **REAL in-process `httptest.Server`** REST stub recording which endpoint ran, and
- the **REAL ZEN CGO binding** driving the condition.

**Commands + result (exact):**
```
CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/spikeflow   => PASS, 8.56s (EXIT 0)
```
Two inputs take DIFFERENT branches, both asserted:
- order 1 => stitched `{"shipping":"expedited"}` **and** exactly `/expedite` hit;
- order 2 => stitched `{"shipping":"standard"}` **and** exactly `/standard` hit.

Nesting depth ≥ 2 (Trigger → Action → Condition → branch Action → Set → Response). The
`SetPath` sibling-coexistence rule the response stitching depends on is unit-tested
(`TestSetPathNoClobber`). The interpreter and ZEN seam shape mirror Slice A §3 and Slice C's
`decision.Evaluator` intent, trimmed to the spike scope (no budget/parallel/forEach/switch/
validation/tracing/versioning/auth).

---

## zen-go v2 vs Slice C — required stitch-time edits (NOT blockers)

Verified by reading v2.1.2 source (`zen.go`, `engine.go`, `decision.go`, `cgo.go`):

| Slice C assumption | Real zen-go v2.1.2 | Adaptation at stitch time |
| --- | --- | --- |
| `Compiled.Eval(ctx, input)` | `Decision.Evaluate(input any)` — **no `context.Context`** | Honor ctx AROUND the call (check `ctx.Err()` before/after; optional hard-timeout goroutine). Slice C's `Timeout` wording must say "no in-eval cancellation." |
| returns `map[string]any` | `*EvaluationResponse{Result json.RawMessage}` | Unmarshal `resp.Result` at the seam → the fixed `decision.Evaluator` contract is preserved. |
| `Close()` | `Dispose()` | Wrapper `Close()` → `Dispose()`. Engine shutdown → `engine.Dispose()`. |
| `NewEngine() (…, error)` | `NewEngine(EngineConfig{}) Engine` (no error; deferred init error) | Surface init error on first `CreateDecision`/`Evaluate`. |
| compiled handle `*zen.Decision` | `zen.Decision` interface from `CreateDecision([]byte)` | Cache value type is `zen.Decision`; `CreateDecision` is the compile step keyed by `(id,version)`. |
| module `github.com/gorules/zen-go` | `.../zen-go/v2` | Pin the `/v2` import path everywhere. |

**Verdict vs ADR-001 / ADR-003 / Slice C:** ADR-001 and ADR-003 **HOLD**. Slice C's public
seam (`decision.Evaluator`) survives **unchanged**; only its *internal* `Compiled` wrapper
needs the adaptations above. These are flagged as spike findings for the Slice C stitch.

---

## Residual risks carried into v1 (none block GO)

1. **R9 base image.** v1 container must be glibc-based (debian-slim/distroless), not Alpine,
   unless `libzen_ffi` is rebuilt for musl. Document in the Dockerfile/ADR-003 notes.
2. **No in-eval cancellation.** A ctx deadline cannot abort a ZEN eval mid-flight; the
   `decision` package honors ctx around the call. Fine for microsecond-scale evals; revisit
   only if a pathological JDM can run long (publish-time validation should bound that).
3. **Cross-compile constrained** to the arches with a vendored `deps/<os>_<arch>` lib, each
   needing the matching cross C toolchain. Build v1 in/for the target arch.
4. **Spike is throwaway.** `internal/zenspike` + `internal/spikeflow` + `cmd/spike` are
   deletable and must not be mistaken for v1 packages (`internal/decision`, `internal/flow`).

---

### Final: **GO.** The two riskiest bets (CGO embed of ZEN, ZEN deciding the real condition)
are proven, and a full nested flow stitches real Postgres + real REST + real ZEN correctly.
Proceed to v1, carrying the Slice C stitch edits and the R9 base-image constraint above.
