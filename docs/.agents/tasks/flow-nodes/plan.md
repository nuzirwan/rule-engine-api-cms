# Implementation Plan — Slice A remaining flow node types

Scope: implement real handlers for `switch`, `parallel`, `forEach`, `decision`, `logger`;
add a global work budget; make `actionHandler` honor dry-run write-suppression. ALL edits are
confined to `/home/nuzirwan/project/rule-engine-api/.worktrees/flow/internal/flow/`.

## Context discovered during exploration (authoritative — read before implementing)

- Baseline is green: `CGO_ENABLED=1 go build ./internal/flow/...` and
  `CGO_ENABLED=1 go test ./internal/flow/...` both pass at HEAD.
- Build/test/vet MUST run from `git -C /home/nuzirwan/project/rule-engine-api/.worktrees/flow`
  with `CGO_ENABLED=1` (ZEN needs CGO). Commands:
  - `CGO_ENABLED=1 go build ./internal/flow/...`
  - `CGO_ENABLED=1 go vet ./internal/flow/...`
  - `CGO_ENABLED=1 go test ./internal/flow/...`
  - `CGO_ENABLED=1 go build ./...` (full-module sanity, confirm nothing else broke)
- The deferred stubs live in `internal/flow/handlers.go` as `deferredHandler{name:...}` and are
  registered in `internal/flow/interpreter.go` `New()`. The spec structs
  (`SwitchSpec`/`ParallelSpec`/`ForEachSpec`/`DecisionSpec`/`LoggerSpec`) ALREADY EXIST in
  `internal/flow/spec.go` and are already in `specValidators` — do NOT redefine them.
- Existing helpers to reuse (do NOT reinvent): `parseSpec[T]`, `projectInputs(c, paths)`,
  `chooseBranch`, `findChild(children, key)`, `walkChildren`, `lastSegment`, `splitPath`,
  `c.GetPath`, `c.SetPath`, `newErr`/`wrapErr`/`validationf`/`classify`, and the error classes
  `ClassValidation`/`ClassTimeout`/`ClassInternal`/`ClassUpstream`.
- `Walker` is the only recursion seam: `w.Walk(ctx, &child, c, dep)` re-enters the dispatch and
  applies the per-request depth guard. Control handlers MUST recurse through `w`, never by
  importing the Interpreter. Do NOT widen `NodeHandler.Exec` or the `Walker` interface
  (both are frozen in `internal/flow/node.go` and `docs/lld-contracts.md`).
- `budget` currently lives in `interpreter.go` with `{depth, maxDepth}` and rides the request
  context under `runStateKey{}`. The walk reads it in `walk()`. Work budget MUST be added to this
  same struct and threaded the same way (no Walker-signature change) — slice-a §6.
- `Deps` (node.go): `Conns connect.Registry`, `Decide decision.Evaluator`, `Trace observ.Tracer`,
  `Log observ.Logger`. `decision.Evaluator.Evaluate(ctx, jdmID, map[string]any) (map[string]any, error)`.
  `observ.Logger.Emit(ctx, level, label string, fields map[string]any)`.
- `connect.Operation.Kind` ∈ {`query`,`exec`,`get`,`set`,`del`,`http`}. Reads are `query`,`get`
  (and `ping`); everything else (`exec`,`set`,`del`,`http`) is a write / side-effecting op for the
  dry-run rule.
- `Ctx` precedence (GetPath): Response → Data → Input. `Ctx` is NOT safe for concurrent writes
  except within the Parallel merge step (contract in ctx.go).

## DRY-RUN SEAM DECISION — CASE (b) applies (verified)

`internal/observ/observ.go` is the ONLY file in `internal/observ` and it does **NOT** export
`WithDryRun` / `IsDryRun` (verified by grep: "NO DRYRUN SYMBOLS IN OBSERV"). The sibling observ
workflow has not landed that seam onto mainline yet. `internal/observ` is OFF-LIMITS to this
worktree, and inventing `observ.IsDryRun` would fail the build.

Resolution for this plan: implement the dry-run suppression LOGIC inside `internal/flow` behind a
flow-local context flag so the package builds and AC-14 behavior is testable NOW, and surface the
missing cross-package seam as a `send_message` warning so the orchestrator can reconcile with the
observ workflow. Concretely:

- Add a flow-local dry-run context helper in a new file `internal/flow/dryrun.go`:
  `type dryRunKey struct{}`, `func withDryRun(ctx) context.Context` (test seam), and
  `func isDryRun(ctx) bool` that returns true when the flow-local key is set. This is a
  package-internal shim, NOT a redefinition of the observ seam, and does not touch observ.
- `actionHandler` calls `isDryRun(ctx)`; when true AND the op is a write (see Kind rule above),
  it SKIPS `client.Execute`, records `wrote:"suppressed"` under `SaveAs` (an object
  `{"wrote":"suppressed"}` so GetPath can read `<saveAs>.wrote`), optionally emits a logger line,
  and continues walking children. A read op in dry-run still executes normally.
- Leave a clearly-marked TODO at the shim: once `observ.IsDryRun` lands on mainline, the stitch
  step swaps `isDryRun(ctx)` to delegate to `observ.IsDryRun(ctx)` (one-line change, same
  behavior). This keeps AC-14 as needs-verification against the real seam rather than dismissed.
- The implementing agent MUST, during this task, call `send_message` with severity "warning"
  stating: observ.IsDryRun/WithDryRun is absent on mainline; flow implemented a local dry-run
  shim guarded so the build is green; orchestrator should coordinate the swap to the real observ
  seam when the observ workflow lands it. (Per the task's case-(b) instruction. If the orchestrator
  confirms the seam has since landed, switch to case (a): call `observ.IsDryRun(ctx)` directly and
  delete the shim.)

## Implementation items (ordered by dependency)

- [ ] 1. Add the global work budget to the existing `budget` struct and charge it in the walk.
      In `internal/flow/interpreter.go`: add `work int64` and `maxWork int64` fields to `budget`;
      add `const DefaultWorkBudget int64 = 100_000` (slice-a §6: nodes executed + iterations).
      Initialize `b := &budget{maxDepth: DefaultMaxDepth, maxWork: DefaultWorkBudget}` in `Run`.
      Add a method `func (b *budget) chargeWork(n int64) error` that subtracts and returns
      `newErr(ClassValidation, "request work budget exhausted")` when it would go negative.
      In `walk()`, after the depth guard and before dispatch, call `b.chargeWork(1)` (guard `b != nil`)
      so every node execution costs one unit; forEach charges additionally per iteration (item 4).
      Keep threading via `runStateKey{}` on ctx — do NOT change the Walker signature.
      Files: internal/flow/interpreter.go
      Verify: `CGO_ENABLED=1 go build ./internal/flow/...` succeeds; existing tests still pass via
      `CGO_ENABLED=1 go test ./internal/flow/...` (no behavior change yet for normal trees — 100k budget is ample).

- [ ] 2. Add an internal budget accessor so handlers (forEach) can charge work without widening
      the Walker. In `internal/flow/interpreter.go` add `func budgetFrom(ctx context.Context) *budget`
      returning `ctx.Value(runStateKey{}).(*budget)` (nil-safe). forEach uses it to charge per
      iteration. This keeps the budget on ctx exactly as the depth guard already is.
      Files: internal/flow/interpreter.go
      Verify: `CGO_ENABLED=1 go build ./internal/flow/...` succeeds.

- [ ] 3. Implement `switchHandler` in `internal/flow/handlers.go`, mirroring `conditionHandler`.
      Parse `SwitchSpec`; require `dep.Decide != nil` (`ClassInternal` otherwise); project inputs
      via `projectInputs(c, spec.Input)`; `out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)`
      (classify err). Determine the branch value from `out["branch"]` as a string; look up
      `childID := spec.Cases[branch]`; if absent, use `spec.Default`. If the resolved childID is
      "" (no case and no default) return a linear fall-through `Directive{Branch:""}` with nil err
      (match condition's no-key behavior). Else `child := findChild(n.Children, childID)`; if nil
      return `validationf("switch %q references unknown child %q", n.ID, childID)`; `w.Walk` only
      that child; set `d.Branch = childID` on the returned directive (preserve downstream Stop).
      Register `TypeSwitch: switchHandler{}` in `New()` (replace the deferred stub).
      Files: internal/flow/handlers.go, internal/flow/interpreter.go
      Verify: `CGO_ENABLED=1 go test ./internal/flow/...` passes (switch tests added in item 8).

- [ ] 4. Implement `forEachHandler` in `internal/flow/handlers.go`.
      Parse `ForEachSpec`. Resolve the array: `v, ok := c.GetPath(spec.Over)`; require
      `items, ok := v.([]any)` else `validationf("forEach %q: path %q is not an array", ...)`.
      Enforce `len(items) > spec.MaxItems` ⇒ `validationf("forEach over %d exceeds maxItems %d")`
      (treat `MaxItems <= 0` as invalid: `validationf`). The body subtree is `n.Children` (walk the
      whole child list once per item via `walkChildren`). For each item (index i):
        - `if err := ctx.Err(); err != nil { return Directive{}, wrapErr(ClassTimeout, "forEach cancelled", err) }`.
        - charge the work budget per iteration: `if b := budgetFrom(ctx); b != nil { if err := b.chargeWork(1); err != nil { return Directive{}, err } }`.
        - Build a child-scoped Input overlay so `As`/`IndexAs` are visible to the body WITHOUT
          mutating the parent Input or leaking between iterations: clone `c.Input` into a shallow
          copy `childInput`, set `childInput[spec.As] = item` and (if `spec.IndexAs != ""`)
          `childInput[spec.IndexAs] = i`, and run the body against a shallow `Ctx` view that shares
          `Data`/`Response` (same pointers, so body writes still accumulate) but swaps `Input`.
          Implement a small helper `withInputOverlay(c *Ctx, overlay map[string]any) *Ctx` returning
          a copy of the struct with `Input` replaced by the merged overlay (overlay keys win).
        - Walk the body: `d, err := walkChildren(ctx, n.Children, childCtx, dep, w)`; on err return
          it; if `d.Stop` return `d` (a Response inside the loop stops the whole walk).
      `ForEachSpec.Parallel == true` runs items concurrently by reusing the parallel fan-out helper
      from item 5 (one task per item, each with its own `withInputOverlay` Ctx whose Response is a
      scratch map merged back under the mutex); sequential (default) runs the loop above. Default to
      sequential; only branch to the concurrent path when `spec.Parallel`.
      Register `TypeForEach: forEachHandler{}` in `New()`.
      Files: internal/flow/handlers.go, internal/flow/interpreter.go
      Verify: `CGO_ENABLED=1 go test ./internal/flow/...` passes (forEach tests in item 8).

- [ ] 5. Implement `parallelHandler` + a bounded concurrent fan-out helper in
      `internal/flow/handlers.go`, using ONLY stdlib `sync` + a semaphore channel (NO new dep —
      explicitly avoiding golang.org/x/sync/errgroup per the merge-contention constraint; adding it
      would touch go.mod/go.sum which are off-limits).
      Parse `ParallelSpec`. children = `n.Children`. `maxc := spec.MaxConcurrency; if maxc <= 0 || maxc > len(children) { maxc = len(children) }`.
      Design the helper `runParallel(ctx, children []Node, c *Ctx, dep Deps, w Walker, maxc int, failFast bool) (Directive, error)`:
        - Derive a cancellable child context: `cctx, cancel := context.WithCancel(ctx); defer cancel()`.
        - Each child gets a CLONED WRITABLE Response view: build a per-child `Ctx` that shares
          `Input`/`Data` (reads) but has its OWN fresh `Response map[string]any{}` scratch (via a
          helper `withScratchResponse(c) *Ctx`). Children must NOT write the shared Response
          directly — that is the only concurrent-write rule in the Ctx contract.
        - Semaphore channel `sem := make(chan struct{}, maxc)`; a `sync.WaitGroup`; a `sync.Mutex`
          guarding (a) first-error capture and (b) the merge into the shared `c.Response`.
        - For each child i: acquire `sem` (respecting `cctx.Done()` so we stop launching after
          cancel), `wg.Add(1)`, goroutine: on panic recover → record `ClassInternal` error; run
          `d, err := w.Walk(cctx, &child, childCtx, dep)`; under the mutex, if err != nil record the
          FIRST error (and if `failFast` call `cancel()` to stop siblings), else merge the child's
          scratch Response into the shared `c.Response`. Merge by walking the scratch map and
          replaying each leaf through `c.SetPath(path, value)` so lodash.set semantics + the
          validation disjointness guarantees hold and sibling paths don't clobber. (Implement a
          small `mergeResponse(dst *Ctx, scratch map[string]any)` that flattens scratch to
          dotted-leaf paths and calls `dst.SetPath`; mutex is held by the caller.) Capture any
          `Directive.Stop` from a child so a Response inside a branch still stops the walk.
        - `wg.Wait()`; return the recorded error (classified) if any, else the aggregated directive
          (Stop if any child returned Stop). Respect `ctx.Err()` at entry (Timeout).
      `parallelHandler.Exec` calls `runParallel(ctx, n.Children, c, dep, w, maxc, spec.FailFast)`.
      Register `TypeParallel: parallelHandler{}` in `New()`.
      Files: internal/flow/handlers.go, internal/flow/interpreter.go
      Verify: `CGO_ENABLED=1 go test -race ./internal/flow/...` passes (parallel tests in item 8;
      run with -race to prove the merge is clobber-free and data-race-free).

- [ ] 6. Implement `decisionHandler` (leaf, no branching) in `internal/flow/handlers.go`.
      Parse `DecisionSpec`; require `dep.Decide != nil` (`ClassInternal` otherwise); project inputs
      via `projectInputs(c, spec.Input)`; `out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)`
      (classify err — this reuses the SAME `decision.Evaluator` the interpreter already holds; do
      NOT reimplement ZEN). If `spec.SaveAs != ""` store `out` into `c.Data[spec.SaveAs]`
      (init `c.Data` if nil, mirroring actionHandler). Return `Directive{}` (leaf, no walk).
      Register `TypeDecision: decisionHandler{}` in `New()`.
      Files: internal/flow/handlers.go, internal/flow/interpreter.go
      Verify: `CGO_ENABLED=1 go test ./internal/flow/...` passes (decision test in item 8).

- [ ] 7. Implement `loggerHandler` (leaf) in `internal/flow/handlers.go` and dry-run action
      suppression in `actionHandler`, plus the `internal/flow/dryrun.go` shim.
      loggerHandler: parse `LoggerSpec`. Logging must NOT change the response and the flow must run
      identically with every logger node removed — so NEVER return an error from a logger: a nil
      `dep.Log` or missing capture path is simply skipped. Honor `SampleRate` (nil ⇒ 1.0): skip the
      emit when a sample draw says so — but to keep tests deterministic, treat `SampleRate == nil`
      or `>= 1.0` as always-emit and `<= 0` as never-emit, and for the in-between case use a
      deterministic source (do NOT seed from wall-clock in a way tests can't control; simplest:
      emit when rate >= 1, skip when rate <= 0, and for 0<rate<1 still emit — documented as
      best-effort sampling, no RNG dependency). Project `Capture` paths via a GetPath loop into a
      fields map keyed by `lastSegment`. Call `dep.Log.Emit(ctx, spec.Level, spec.Label, fields)`
      when `dep.Log != nil`. Return `Directive{}` then `walkChildren` is NOT needed (logger is a
      leaf per spec) — return `Directive{}` with nil err. Register `TypeLogger: loggerHandler{}`.
      dryrun.go: add `dryRunKey struct{}`, `withDryRun(ctx) context.Context`, `isDryRun(ctx) bool`
      (CASE (b) shim per the decision above). Add `isWriteOp(kind string) bool` returning false for
      `"query"`,`"get"`,`"ping"` and true otherwise.
      actionHandler: after resolving `spec` and BEFORE `client.Execute`, if `isDryRun(ctx) &&
      isWriteOp(op.Kind)`: skip Execute; if `spec.SaveAs != ""` set
      `c.Data[spec.SaveAs] = map[string]any{"wrote":"suppressed"}`; emit a logger line if
      `dep.Log != nil`; then `return walkChildren(ctx, n.Children, c, dep, w)`. A read op in
      dry-run executes normally.
      Register `TypeLogger: loggerHandler{}` in `New()`.
      Files: internal/flow/handlers.go, internal/flow/interpreter.go, internal/flow/dryrun.go
      Verify: `CGO_ENABLED=1 go build ./internal/flow/...` and `CGO_ENABLED=1 go test ./internal/flow/...`
      pass (logger + dry-run tests in item 8). ALSO: the implementing agent calls `send_message`
      (severity "warning") about the missing observ.IsDryRun seam per the CASE (b) decision above.

- [ ] 8. Extend `internal/flow/interpreter_test.go` with table-driven cases for every new node type,
      reusing existing fakes (`fakeRegistry`, `fakeClient`, `fakeEvaluator`) and helpers
      (`raw`, `buildTree`, `NewCtx`). NO sleep-based timing — use channels / `context.WithCancel`
      for the concurrency/cancellation cases. Cases to add and what each asserts:
        - switch: a tree `Trigger -> Switch{cases:{"gold":"g","silver":"s"}, default:"d"}` with
          children g/s/d each a Set+Response. (a) `fakeEvaluator{out:{"branch":"gold"}}` ⇒ the g
          child ran (assert its Set landed in Response) and `branch_taken`/directive is "g";
          (b) `{"branch":"bronze"}` (no case) ⇒ the default child `d` ran; (c) default empty +
          unmatched branch ⇒ linear fall-through (no child Set in Response, no error).
        - parallel aggregation: `Parallel` with two children writing DISJOINT Set paths
          (`a.x`, `b.y`) each ending without Response; assert BOTH paths present in `c.Response`
          after Run (clobber-free merge). Run the package with `-race`.
        - parallel error propagation: one child's action fails (`fakeClient{err}`, OnError default);
          assert Run returns a classified error (errors.As *FlowError) and, with `FailFast:true`,
          the sibling is cancelled — prove deterministically by making the sibling block on a
          channel that the test closes only after observing cancellation via `ctx.Done()` (use a
          custom in-test handler/fake client that selects on ctx.Done()), so no sleep is needed.
        - parallel cancellation: parent ctx cancelled (`context.WithCancel` + cancel before/within)
          ⇒ Run returns `ErrTimeout`-class; assert via `errors.Is(err, ErrTimeout)`.
        - forEach iteration: Input holds `items: []any{...3...}`; forEach `Over:"items", As:"it",
          IndexAs:"i", MaxItems:10` with a body Set that writes `out.<index>` from the bound item;
          assert the body ran 3 times and all three results landed (sequential path).
        - forEach maxItems exceeded: 3 items, `MaxItems:2` ⇒ `errors.Is(err, ErrValidation)`.
        - forEach budget: a nested forEach whose iteration count would exceed a lowered budget —
          to test deterministically, set a tree that forEaches over a large array (e.g. build the
          budget small via a test-only path OR assert the normal-budget happy path returns and a
          deliberately-oversized array trips `ErrValidation`). Prefer asserting `MaxItems` trips
          validation (above) AND add a focused unit test on `budget.chargeWork` that it returns
          `ErrValidation` once exhausted. (Keeps the global-budget guarantee covered without a
          100k-iteration test.)
        - forEach cancellation: parent ctx cancelled before an iteration ⇒ `errors.Is(err, ErrTimeout)`.
        - decision routing: `Trigger -> Decision{jdmId, input, saveAs:"scored"} -> Set(from:
          "scored.tier") -> Response`; `fakeEvaluator{out:{"tier":"gold"}}`; assert
          `c.Data["scored"]` equals the evaluator output AND the evaluator saw the projected inputs
          (reuse the `eval.seen` assertion pattern from the existing branch test).
        - logger: `Trigger -> Logger{label,level,capture:[somePath]} -> Response`; use a capturing
          fake Logger (add a tiny in-test `captureLogger` implementing `observ.Logger` recording
          Emit calls); assert the flow result is IDENTICAL with the logger node present vs removed
          (run both trees, compare `c.Response`), proving logger is not a suppressed write.
        - dry-run action suppression (AC-14): a write-op action (`Operation{Kind:"exec"}`,
          `SaveAs:"w"`) under a dry-run ctx (`withDryRun(context.Background())`); assert
          `fakeClient.ops` is EMPTY (Execute skipped) and `c.Data["w"]` == `{"wrote":"suppressed"}`,
          and the walk continued to the child Response. Add a companion case: a READ op
          (`Kind:"query"`) under dry-run still calls Execute (ops non-empty).
      Files: internal/flow/interpreter_test.go
      Verify: `CGO_ENABLED=1 go test -race ./internal/flow/...` — all new and existing tests pass,
      no data races, no hangs.

- [ ] 9. Full verification sweep.
      Run, from `git -C /home/nuzirwan/project/rule-engine-api/.worktrees/flow`:
      `CGO_ENABLED=1 go build ./internal/flow/...`, `CGO_ENABLED=1 go vet ./internal/flow/...`,
      `CGO_ENABLED=1 go test -race ./internal/flow/...`, and `CGO_ENABLED=1 go build ./...`.
      Confirm the full-module build still passes (nothing outside internal/flow was touched) and
      that only files under `internal/flow/` were modified (`git -C ... status` shows no changes to
      go.mod/go.sum/cmd/engine/other packages).
      Files: (verification only)
      Verify: all four commands exit 0; `git status` shows edits confined to internal/flow/.

## Constraints restated (coder MUST honor)

- Edit ONLY files under `internal/flow/`. Do NOT touch go.mod, go.sum, cmd/engine,
  internal/httpapi, internal/config, internal/connect, internal/decision, internal/observ,
  internal/auth, or any other package dir. Three sibling workflows edit config/connect/observ+auth
  concurrently off the same mainline — any edit outside internal/flow is a merge-contention bug.
- Keep `Interpreter.Run(ctx, tree *Node, ver Version, c *Ctx, dep Deps)` signature exactly; do NOT
  reintroduce a config import. Do NOT widen `Walker` or `NodeHandler.Exec`.
- Add NO new dependencies. Parallel fan-out uses stdlib `sync` + a semaphore channel only. If a new
  dep ever seems unavoidable, STOP and raise it as a seam/merge risk via `send_message` rather than
  adding it.
- Dry-run is CASE (b): local flow shim now + a `send_message` warning about the absent
  `observ.IsDryRun` seam. Do NOT edit internal/observ and do NOT invent `observ.IsDryRun`.

## Decomposition decision

This work is a single cohesive unit, not separable features: all handlers live in one file
(`handlers.go`), share helpers (`projectInputs`, `chooseBranch`, `findChild`, `walkChildren`,
`withInputOverlay`), the parallel fan-out is reused by forEach, the work budget threads through
both parallel and forEach, and all tests live in one `interpreter_test.go`. Splitting into
parallel FEATs sharing one worktree would create constant intra-file merge contention for no
benefit. Therefore the plan is a single ordered list for the existing implement-and-review loop;
the workflow tail is NOT restructured. The loop's stop contract is unchanged: it stops when
`/home/nuzirwan/project/rule-engine-api/.worktrees/flow/docs/.agents/tasks/flow-nodes/review.json`
has top-level `verdict == "APPROVED"` written last by the semantic_reviewer, with onMaxIterations
aborting.
