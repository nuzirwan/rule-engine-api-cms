# Phase 0 De-Risking Spike — Gate Review

The spike set out to answer one question honestly: do the two riskiest architectural bets
(embed `zen-go` via CGO per ADR-003/R9, and let ZEN decide the hardest real condition per
ADR-001/R7) survive contact with the real binding, before any v1 interpreter work. It did
the job. The real `github.com/gorules/zen-go/v2 v2.1.2` binding is pinned and exercised
end to end — not mocked — the representative JDM is evaluated by a table-driven test with a
determinism check, and a minimal tree-walking interpreter runs one nested flow against a
real ephemeral Postgres container and an in-process `httptest.Server`, taking two different
branches. The report's GO verdict is supported by the recorded evidence, and the one place
the API diverges from Slice C's assumptions is surfaced loudly rather than hidden — which is
exactly what HLD R9/ADR-003 wanted out of this spike.

Watch for: (confirmed) AC-S1's "runnable binary in the target image / `docker run` the image"
clause from HLD §10 was characterized by `ldd` + static-link analysis on the host, not by
actually building and `docker run`-ing a container image — the report is honest about this and
converts it into a glibc-base-image action item, so it is a scope note, not an overclaim.
(confirmed) The zen-go API diverges from Slice C (no ctx arg on `Evaluate`, `json.RawMessage`
result, `Dispose` not `Close`, no error from `NewEngine`, `/v2` module path); all are reported
as stitch-time edits and none invalidate the fixed `decision.Evaluator` seam.

**Verdict**: APPROVED

## High-level view

AC-S1 is faithfully validated. `go.mod`/`go.sum` pin the real `zen-go/v2 v2.1.2`, the module
is in the build cache, and the wrapper imports and calls the genuine CGO API (`zen.NewEngine`,
`eng.CreateDecision`, `dec.Evaluate`, `Dispose`) under a `//go:build cgo` tag. The non-CGO
`zen_stub.go` compiles but refuses at runtime, so there is no silent no-op path. "Runs" is
demonstrated by `go run ./cmd/spike` printing the three canonical outcomes, and the R9 CGO tax
(vendored `libzen_ffi.a`, dynamic glibc link, non-portability to musl/Alpine) is characterized
with `ldd` evidence. The only soft spot is that the HLD's literal "docker run the image" was
not performed; the report converts that into a documented base-image constraint rather than
pretending an image was shipped.

AC-S2 is faithfully validated. The JDM in `testdata/order.jdm.json` encodes the exact required
condition (`amount > 1000 AND status == 'paid' => 'expedited'`, else `'standard'`, hitPolicy
`first`), and `TestDecisionTable` asserts five inputs including both sides of the strict-`>`
boundary (1000 → standard, 1001 → expedited). `TestDeterminism` runs 50 repeats across two
independently compiled decisions, covering the determinism half of the criterion.

AC-S3 is faithfully validated. `TestNestedFlowEndToEnd` builds a real nested tree
(Trigger → Action(pg read) → Condition(ZEN) → branch Action(REST) → Set → Response), runs it
synchronously, and asserts both the stitched `{"shipping": ...}` and the exact REST path hit
for two orders that take different branches. Postgres is a real `docker run postgres:16`
container seeded via `pgx` and torn down on cleanup; the REST stub is a genuine in-process
`httptest.Server`; the condition is driven by the real ZEN binding. The branch is selected
from ZEN's output value, not hardcoded, so the two-branch assertion is meaningful.

The report's per-AC PASS and overall GO are consistent with the verification note and the
commit message, and the zen-go/Slice C divergences are presented as adaptable stitch edits with
the seam preserved. Nothing in the diff reaches beyond spike scope (no config store, cache,
versioning, auth, OTel), and no third-party router appears anywhere.

<details>
<summary>Issues (2)</summary>

1. **AC-S1 "docker run the image" not literally performed** — HLD §10 AC-S1 asks for a runnable
   binary proven in the target image via `docker run`; the spike proved compile+run+link on the
   host (`go run`, `ldd`, static-link warnings) and turned the image question into a glibc-base
   action item. Honest and sufficient for a GO, but the v1 CI must actually build and run the
   container image on a glibc base to close R9 fully. Non-blocking.
2. **zen-go v2 diverges from Slice C's `Compiled`/`Evaluator`** — `Evaluate` takes no `ctx`,
   returns `*EvaluationResponse{Result json.RawMessage}`, uses `Dispose()` not `Close()`,
   `NewEngine` returns no error, and the module path is `/v2`. All are reported as stitch-time
   edits that keep the fixed `decision.Evaluator` contract; carry them into the Slice C stitch.
   Non-blocking (surfacing them is the win).

</details>

<details>
<summary>Details</summary>

### AC-S1 — real CGO binding, pinned, compiled and run

The binding is the real one, not a stub standing in for it. `go.mod` requires
`github.com/gorules/zen-go/v2 v2.1.2`, `go.sum` carries its hash, and the module is present in
the module cache. `internal/zenspike/zen.go` is tagged `//go:build cgo` and calls the genuine
API surface — `zen.NewEngine(zen.EngineConfig{})`, `eng.CreateDecision(jdm)`, `dec.Evaluate(input)`,
`Dispose()`. There is no mock engine or hand-rolled decision-table evaluator anywhere in the
tree; the branch logic in `spikeflow` consumes whatever ZEN returns.

"Runs" (not just compiles) is demonstrated: the verification note records
`CGO_ENABLED=1 go run ./cmd/spike` emitting `expedited / standard / standard` and `AC-S1 OK`
with exit 0, and `cmd/spike/main.go` is the thin driver that loads the JDM and evaluates the
three cases. The `zen_stub.go` guard (`//go:build !cgo`) matters here: it compiles so a pure-Go
vet lane works, but every entrypoint returns `ErrNoCGO`, and the non-CGO binary exits 1 with a
clear message — so there is no path where ZEN silently appears to work without the native lib.

The R9 CGO tax is characterized with evidence rather than waved away. `ldd` on the built binary
shows a dynamic glibc link (`libc.so.6`, `libm.so.6`, `libgcc_s.so.1`); a fully-static link emits
the `getpwuid_r`/`getaddrinfo` glibc warnings and is called out as non-portable to musl/Alpine;
the native `libzen_ffi.a` ships vendored in the module so there is no Rust build step. The one
gap against the literal HLD wording — "producing a runnable binary in the target image… `docker
run` the image" — is that the proof is host-side (`go run` + `ldd`), not an actual container run.
The report does not hide this; it converts it into an explicit v1 action item (glibc base image,
debian-slim/distroless, not Alpine). For a Phase 0 compile-and-run gate that is a faithful call,
and leaving the actual image build to v1 CI is reasonable, but it should be closed there.

### AC-S2 — the real condition as a JDM, table-driven and deterministic

`testdata/order.jdm.json` is a proper JDM graph (inputNode → decisionTableNode → outputNode with
edges), and the decision table encodes the required condition exactly: rule 1 is `amount > 1000`
AND `status == 'paid'` → `'expedited'`, with a fallthrough → `'standard'` under hitPolicy `first`.
`TestDecisionTable` drives five inputs through the compiled decision, and crucially it brackets
the strict-`>` boundary on both sides (1000 → standard, 1001 → expedited), which is the one place
an off-by-one in the expression would show. `TestDeterminism` closes the determinism half of the
criterion: 50 repeats on one compiled decision and a second independently compiled one, asserting
the output never drifts. This is driven through the same `Eval` seam the production decision
package will use, so it also exercises the `json.RawMessage` → `map[string]any` unmarshal.

### AC-S3 — nested flow over real Postgres and real REST

`TestNestedFlowEndToEnd` (`//go:build integration && cgo`) builds the full nested tree in
`spikeflow`: Trigger → Action(pg read) → Condition(ZEN) → { expedited: Action(POST /expedite) ;
standard: Action(POST /standard) } → Set(shipping) → Response. This is ≥2 nesting depth, and the
Set/Response live under the branch Action so the stitched field only appears on the path that ran.

The three "real" requirements all hold. Postgres is a genuine ephemeral container —
`docker run -d -P postgres:16`, host port resolved via `docker inspect`, readiness polled with a
real `pgx.Connect`/`Ping`, seeded with two rows, and torn down with `docker rm -f` in cleanup.
There is no in-memory fake standing in for the database. The REST actions hit an in-process
`httptest.NewServer` that records `r.URL.Path`, so the test can assert exactly one endpoint was
called per run. The condition is the real ZEN binding (`compiled.Eval`), and the branch is chosen
from ZEN's returned `shipping` value in `interpreter.go` (`branch == n.TrueKey` → true child, else
false child) — the branch is not short-circuited by the test. Both sub-cases are asserted on two
axes: order 1 {1500,paid} → stitched `{"shipping":"expedited"}` and exactly `/expedite` hit;
order 2 {500,paid} → `{"shipping":"standard"}` and exactly `/standard` hit. Two inputs, two
different branches, both the response body and the side-effecting call verified.

The `SetPath` sibling-coexistence rule the stitching depends on is independently unit-tested in
`TestSetPathNoClobber` (siblings coexist; a scalar in an intermediate position is a conflict).
Not covered: the integration test asserts only the two seeded orders — a third input exercising
a malformed row or a ZEN eval error is out of scope for the gate but worth noting for v1.

### zen-go v2 vs Slice C — surfaced, not buried

The spike's most valuable output is that it caught the API drift HLD R9/ADR-003 was worried
about and reported it plainly. The real v2 API differs from Slice C's assumed `Compiled` in five
concrete ways: `Evaluate` takes no `context.Context` (so ctx can only be honored around the call,
not mid-eval — the wrapper checks `ctx.Err()` before and after); it returns
`*EvaluationResponse{Result json.RawMessage}` rather than `map[string]any` (unmarshalled at the
seam); lifecycle is `Dispose()` not `Close()`; `NewEngine` returns no error (init failure defers
to first use); and the module path is the versioned `/v2`. The report and verification note both
present these as stitch-time edits that keep the fixed `decision.Evaluator` contract intact, and
the wrapper in `zen.go` already implements the adaptation. This is the honest "build/API surprise"
the gate wanted on the table, so it strengthens the GO rather than undermining it. The one item
worth carrying forward beyond a code edit is the "no in-eval cancellation" property: Slice C's
`Timeout` semantics need to reflect that a ctx deadline cannot abort a running eval, which matters
if a pathological JDM can run long (publish-time validation should bound that).

### Scope and router discipline

The diff stays inside spike scope — only `internal/zenspike`, `internal/spikeflow`, `cmd/spike`,
the two deps, and docs. No config store, cache, versioning, auth, or OTel leaked in, matching the
plan's scope guard. A grep across the Go sources finds no `go-chi`, `gorilla/mux`, `gin`, or
`echo` import; the only HTTP in play is stdlib `net/http`/`httptest`, satisfying the
stdlib-only constraint.

### Verification evidence

The coder recorded exact commands and outputs in `verification.md` and the commit message:
`CGO_ENABLED=1 go build ./...` (exit 0), `go run ./cmd/spike` (the three outcomes), `go test
./internal/zenspike` (table + determinism PASS), `go test -tags 'integration cgo'
./internal/spikeflow` (PASS, ~8.3–8.6s), the `ldd`/static-link/non-CGO lane evidence, and
`go mod tidy` reporting no changes. Per the gate instructions I did not re-run the suites; the
evidence is specific, internally consistent, and matches the source I read, so there was no
articulable doubt requiring a spot-check beyond confirming the dependency is the real pinned
module and that no third-party router is present (both confirmed).

</details>

<details>
<summary>File map</summary>

- `go.mod` / `go.sum` — pin `zen-go/v2 v2.1.2` and `pgx/v5 v5.6.0` (+ transitive).
- `cmd/spike/main.go` — AC-S1 smoke driver: load JDM, evaluate three cases, print `AC-S1 OK`.
- `internal/zenspike/zen.go` — `//go:build cgo` wrapper over the real zen-go v2 API.
- `internal/zenspike/zen_stub.go` — `//go:build !cgo` guard; compiles, refuses at runtime.
- `internal/zenspike/testdata/order.jdm.json` — the representative decision-table JDM.
- `internal/zenspike/zen_test.go` — AC-S2 table-driven + determinism tests.
- `internal/spikeflow/nodes.go` — the trimmed node taxonomy for the spike flow.
- `internal/spikeflow/interpreter.go` — the tree-walk, Ctx, SetPath/GetPath stitching.
- `internal/spikeflow/interpreter_inmem_test.go` — CGO/Docker-free walk + SetPath tests.
- `internal/spikeflow/interpreter_integration_test.go` — AC-S3 end-to-end (real PG + httptest + ZEN).
- `docs/.spike/plan.md`, `docs/.spike/verification.md`, `docs/phase0-spike-report.md` — plan, evidence, verdict.

Full diff: `git -C .worktrees/phase0-spike diff mainline...spike/phase0`.

</details>
