# Increment report — internal/flow node taxonomy completion

Branch: `feat/flow-nodes` (worktree `.worktrees/flow`, off mainline)
Package: `internal/flow` · Go 1.26 · CGO_ENABLED=1
Scope: implement the five remaining node handlers + loop/budget safety + extend
`ValidateTree`. No other package touched.

## What was built

Thin slice shipped real `trigger/action/condition/set/response` handlers plus
Validation-returning stubs for `switch/parallel/forEach/decision/logger`. This
increment replaces those five stubs with real handlers and adds the structural
validator, building on the existing interpreter/Walker/Ctx/budget seams — the
interpreter, the working handlers, and the frozen contracts were not rewritten.

### New files
- `internal/flow/control_handlers.go` — `switchHandler`, `parallelHandler`,
  `forEachHandler`, `decisionHandler`, `loggerHandler` + merge/binding helpers.
- `internal/flow/validate.go` — `ValidateTree` structural checker (slice-a §7)
  with `RefResolver` injection; pure, collect-all.
- `internal/flow/control_handlers_test.go` — AC-2 / AC-3 / AC-17 table tests.
- `internal/flow/validate_test.go` — ValidateTree pass/fail rows per rule.

### Edited files
- `internal/flow/interpreter.go` — budget made concurrency-safe for fan-out:
  `work` is now an `atomic.Int64` shared ceiling; recursion `depth` moved onto
  the context (`depthKey`) so concurrent parallel/forEach sibling branches each
  track their own depth and never race on a shared counter. (Found and fixed via
  `go test -race`.)
- `internal/flow/ctx.go` — added `Ctx.cloneWritable()` giving each
  parallel/forEach child a scratch `Response` map sharing read-only Input/Data.
- `internal/flow/interpreter_test.go` — the pre-existing
  `TestInterpreter_DeferredNodeIsValidation` used a `parallel` node to assert the
  stub refusal; since `parallel` is now real, it was repointed at `sequence`
  (which remains the deferred stub in the dispatch table).

## Handler behavior (slice-a §2–3)

- **switch** — projects `Input`, evaluates `Deps.Decide`, looks up the output
  `branch` in `Cases` else falls back to `Default`, walks the one chosen child
  via the Walker, records `Directive.Branch`. No match + no default = linear
  fall-through.
- **parallel** — bounded-concurrency fan-out (`MaxConcurrency`, 0 => all); each
  child walks a `cloneWritable()` scratch view; disjoint `Response` paths merge
  back under one mutex via `SetPath` (no-clobber preserved). `FailFast` cancels
  siblings on the first error.
- **forEach** — iterates the `Over` array, binds `As`/`IndexAs` into a
  per-iteration Input overlay, walks the body subtree per item; enforces
  `MaxItems` + per-request work budget + ctx deadline; merges each iteration's
  Response back.
- **decision** — evaluates `Deps.Decide`, stores the output map under
  `Data[SaveAs]`; no branching (leaf).
- **logger** — emits via `Deps.Log.Emit` with capture paths, level, and
  deterministic `SampleRate`; never writes Response, never stops the walk. Uses
  the existing `observ.Logger` seam (internal/observ untouched).

Budget (§6): depth ceiling (`DefaultMaxDepth=64`), work ceiling
(`DefaultWorkBudget=100_000`, one unit per node walked + one per forEach
iteration, atomic), and ctx-deadline/cancellation checks — all terminate with a
classified error (`ClassValidation` / `ClassTimeout`), never a hang (AC-17).

`ValidateTree` (§7) covers: single Trigger root; known types + strict spec
decode; control-have-children / leaves-have-none; condition/switch keys point at
real children; bounded static depth; forEach `MaxItems>0` + `Over`/`As`;
parallel sibling-write disjointness; exactly-one terminal Response per path;
unique IDs; well-formed Set targets.

## AC → test mapping

| AC | Test |
|----|------|
| AC-2 nested branches + switch default | `TestSwitch_NWayAndDefault`, `TestSwitch_NestedCondition` |
| AC-3 parallel disjoint merge, no clobber | `TestParallel_DisjointMergeNoClobber`, `TestParallel_FailFastCancelsSiblings` |
| AC-17 forEach maxItems | `TestForEach_MaxItemsExceeded` |
| AC-17 work-budget-exhausted | `TestForEach_WorkBudgetExhausted` |
| AC-17 ctx-deadline => classified, not hang | `TestForEach_CtxDeadlineClassifiedNotHang` |
| decision/logger leaves | `TestDecision_StoresOutput`, `TestLogger_EmitsCaptureAndNeverAltersResponse`, `TestLogger_SampleRateZeroSkips` |
| ValidateTree pass/fail per rule | `TestValidateTree` (13 rows) |

## Verification (real output)

```
$ CGO_ENABLED=1 go build ./internal/flow
build exit: 0

$ CGO_ENABLED=1 go vet ./internal/flow
vet exit: 0

$ CGO_ENABLED=1 go test ./internal/flow
ok  	nzr-rules-engine/internal/flow	0.015s
test exit: 0
```

Race-checked (parallel fan-out):

```
$ CGO_ENABLED=1 go test -race ./internal/flow
ok  	nzr-rules-engine/internal/flow	1.056s
```

Whole module still builds:

```
$ CGO_ENABLED=1 go build ./...
build-all exit: 0
```

All flow tests PASS (new + pre-existing): switch (incl. default + nested),
parallel (disjoint merge + fail-fast), forEach (iterate/bind, maxItems,
work-budget, ctx-deadline), decision, logger, ValidateTree (13 rows), plus the
original interpreter/ctx tests.
