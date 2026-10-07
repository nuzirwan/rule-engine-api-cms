# Map/Reduce Node Types for Flow Engine

Adds `map` and `reduce` node types to complete the flow engine's collection operations. Map transforms each item via a ZEN decision and produces an output array of the same length. Reduce folds an array into a single accumulator value using the `{accumulator, current, index}` contract. Both follow the existing filter/find pattern exactly: same validation order, same budget guard enforcement, same projectItemInputs reuse.

**Watch for:** Nothing blocking. The implementation is faithful to the design and the existing filter/find pattern.

**Verdict**: APPROVED

## High-level view

The implementation adds two new node types that slot cleanly into the existing collection-operation taxonomy. Both handlers follow the filter/find template line-for-line: spec parsing → required-field validation (maxItems > 0, no defaulting) → source resolution → budget guard check before iteration → ZEN invocation with projectItemInputs for input projection → result storage. The reduce handler builds the `{accumulator, current, index}` map per iteration with index as float64 for JSON compatibility.

Validation and spec registration are consistent with filter/find: leafTypes registration, ValidateTree switch cases with identical field checks, specValidators entries using parseSpec. No CMS changes — this is engine-only as specified.

Test coverage is comprehensive: 18 tests covering empty array, single item, transform/sum/max/build-object use cases, maxItems exceeded, source not found/not-array, input projection, ZEN returns nil, context cancellation, and the float64 index contract. The commit message records passing build and test output.

<details>
<summary>Issues (0)</summary>

No blocking issues identified. The implementation matches the design document, follows the existing filter/find pattern, and passes all tests.

</details>

<details>
<summary>Details</summary>

## Handler implementations follow filter/find exactly

The mapHandler and reduceHandler in control_handlers.go mirror the filterHandler structure (lines 448-515):

1. **Validation order matches filter/find**: maxItems check first, then over/jdmId/saveAs, then dep.Decide nil check. No defaulting of maxItems — the handlers require explicit `maxItems > 0` (lines 589-592 for map, 644-647 for reduce), matching filter line 459 and find line 504.

2. **Source resolution and budget guard**: GetPath → []any assertion → len check before iteration. The budget guard fires before any ZEN call, so maxItems exceeded returns ErrValidation with no side effects.

3. **Iteration with cancellation**: Both handlers check ctx.Err() at the top of each iteration and wrap the error with ClassTimeout, same as filter/find.

4. **projectItemInputs reuse**: Map calls `projectItemInputs(item, spec.Input)` directly (line 614). Reduce calls it for the `current` field (line 667), so non-map items become `{"item": <value>}` and map items with Input specified get field projection. This consistency was explicitly requested in the design doc's review response section.

## Reduce ZEN input contract

The reduce handler builds the ZEN input map with:
```go
in := map[string]any{
    "accumulator": acc,
    "current":     projectItemInputs(item, spec.Input),
    "index":       float64(i),
}
```

Index is float64 for JSON compatibility (Go's encoding/json decodes numbers as float64). The test `TestReduce_IndexIsFloat64` (lines 1079-1096) verifies this contract by checking the captured index values.

## Map output contract

Map stores the entire ZEN output map per item, not a single extracted value. The result array length equals input length. Test `TestMap_TransformItems` (lines 738-762) verifies this by asserting each result element is `map[string]any{"price": <value>}`.

## Empty array behavior

- **Map**: returns `[]` with zero ZEN calls. Test `TestMap_EmptyArray` verifies `len(result) == 0` and `len(eval.seen) == 0`.
- **Reduce**: returns `initialValue` (or nil if unspecified) with zero ZEN calls. Tests `TestReduce_EmptyArray` and `TestReduce_EmptyArrayNoInitial` cover both cases.

## Validation in validate.go

The ValidateTree switch cases for TypeMap and TypeReduce (lines 185-207) check:
- `checkJDM` for jdmId reference resolution
- `spec.MaxItems <= 0` → validation issue
- `spec.Over == ""` → validation issue
- `spec.SaveAs == ""` → validation issue

This matches the filter/find validation exactly (lines 163-183 in the original file).

## Spec definitions

MapSpec and ReduceSpec in spec.go follow the design:
- Both have `Over`, `JDMID`, `Input`, `SaveAs`, `MaxItems`
- ReduceSpec adds `InitialValue any` with `omitempty` tag
- Both registered in specValidators map
- Comments document the leaf-node nature and budget guard purpose

## Test coverage summary

Map tests (9):
- TestMap_EmptyArray — zero items, zero ZEN calls
- TestMap_SingleItem — one item, one ZEN call
- TestMap_TransformItems — three items with input projection, verifies ZEN output map storage
- TestMap_MaxItemsExceeded — 5 items with maxItems=3, ErrValidation
- TestMap_SourceNotFound — missing path, ErrValidation
- TestMap_InputProjection — verifies only specified fields reach ZEN
- TestMap_ZENReturnsNonMap — ZEN returns nil, stored as-is

Reduce tests (11):
- TestReduce_EmptyArray — with initialValue, returns initialValue
- TestReduce_EmptyArrayNoInitial — returns nil
- TestReduce_SingleItem — one ZEN call
- TestReduce_SumValues — sum accumulator pattern
- TestReduce_FindMax — max accumulator pattern
- TestReduce_BuildObject — object-building pattern
- TestReduce_MaxItemsExceeded — 5 items with maxItems=3, ErrValidation
- TestReduce_SourceNotFound — missing path, ErrValidation
- TestReduce_SourceNotArray — non-array source, ErrValidation
- TestReduce_ZENInputShape — verifies {accumulator, current, index} structure
- TestReduce_IndexIsFloat64 — verifies index type
- TestReduce_CtxCancelled — cancelled context returns ErrTimeout

## Verification evidence

The commit message records:
```
Verification:
  cd engine && CGO_ENABLED=1 go build ./... => OK (exit 0)
  cd engine && CGO_ENABLED=1 go test ./...  => ALL PASS (exit 0)
  nzr-rules-engine/internal/flow: ok 0.035s
```

</details>

<details>
<summary>File map (6 files)</summary>

| File | What changed |
|------|--------------|
| `node.go` | Added `TypeMap`, `TypeReduce` constants after `TypeFind` |
| `spec.go` | Added `MapSpec`, `ReduceSpec` structs; registered in `specValidators` |
| `control_handlers.go` | Added `mapHandler`, `reduceHandler` (128 lines) following filter/find pattern |
| `interpreter.go` | Registered `TypeMap: mapHandler{}`, `TypeReduce: reduceHandler{}` in `New()` |
| `validate.go` | Added to `leafTypes`; added validation cases for both types |
| `control_handlers_test.go` | Added 18 tests plus test helpers (474 lines) |

Full diff: `git diff main` (or check worktree commit)

</details>
