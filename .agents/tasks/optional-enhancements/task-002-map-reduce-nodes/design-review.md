# Design Review: Map/Reduce Node Types (TASK-002)

**Reviewer**: Design Review Subagent  
**Date**: Auto-generated  
**Design Document**: `/home/nuzirwan/project/rule-engine-api/.agents/tasks/optional-enhancements/task-002-map-reduce-nodes/design.md`

---

## Verified Assumptions

The following claims in the design were verified against the actual source code:

1. **Filter/find handler pattern (lines 320-430)**: The design correctly describes the filter handler starting at line ~320 and find handler at line ~370. The code shows:
   - `parseSpec[FilterSpec](n.Spec)` with strict unknown-field rejection ✓
   - Validation order: `spec.MaxItems <= 0` first, then `spec.Over == ""`, `spec.JDMID == ""`, `spec.SaveAs == ""` ✓
   - Source resolution via `c.GetPath(spec.Over)` with `[]any` type assertion ✓
   - Budget guard: `len(items) > spec.MaxItems` returns validation error before iteration ✓
   - Context cancellation check in loop: `ctx.Err()` ✓
   - ZEN invocation: `dep.Decide.Evaluate(ctx, spec.JDMID, in)` with `projectItemInputs` ✓
   - Result storage: `c.Data[spec.SaveAs] = result` ✓
   - Leaf behavior: returns `Directive{}` with no children walked ✓

2. **projectItemInputs helper (lines ~421-430)**: Verified exactly as described in design:
   ```go
   func projectItemInputs(item any, inputFields []string) map[string]any {
       m, isMap := item.(map[string]any)
       if !isMap || len(inputFields) == 0 {
           return map[string]any{"item": item}
       }
       out := make(map[string]any, len(inputFields))
       for _, k := range inputFields {
           if v, ok := m[k]; ok {
               out[k] = v
           }
       }
       return out
   }
   ```

3. **No defaulting of maxItems**: Confirmed in filter (line ~325) and find (line ~379): `if spec.MaxItems <= 0` returns validation error immediately with no defaulting.

4. **Validation in validate.go**: Confirmed filter/find validation cases at lines ~156-175 follow the pattern the design proposes for map/reduce.

5. **Node type constants**: `TypeFilter` and `TypeFind` are defined in `node.go` as expected.

6. **leafTypes map**: Confirmed `TypeFilter: true` and `TypeFind: true` in validate.go.

7. **specValidators map**: Confirmed filter/find entries in spec.go.

8. **Handler registration in interpreter.go**: Confirmed `TypeFilter: filterHandler{}` and `TypeFind: findHandler{}` in `New()`.

9. **Existing test patterns**: The test file shows `matchEvaluator` pattern for filter/find tests that the design's test helpers follow.

---

## Unverified/Wrong Assumptions

None. All design claims about the existing codebase were verified to be accurate.

---

## Findings

### Finding #1 — NIT: mapHandler stores entire ZEN output map but test expectation is ambiguous

**Location**: Design section "Key behaviors" under mapHandler and test `TestMap_TransformItems`

**Problem**: The design states "stores entire ZEN output map per item" and shows the test expecting `[{"price":110},{"price":220},{"price":330}]`. This is correct. However, the test description says "ZEN returns `{"price": input.item.p * 1.1}`" — this notation mixes ZEN pseudo-code with expectation.

**Fix**: Clarify the test setup:
```
| `TestMap_TransformItems` | 3 items `[{"p":100},{"p":200},{"p":300}]`, ZEN configured to return `{"price": <item.p * 1.1>}` | Result is `[{"price":110},{"price":220},{"price":330}]` — full ZEN output maps |
```

---

### Finding #2 — NIT: ReduceSpec InitialValue JSON tag inconsistency with design philosophy

**Location**: ReduceSpec struct definition

**Problem**: The design uses `omitempty` on `InitialValue`:
```go
InitialValue any `json:"initialValue,omitempty"`
```

This is fine, but the handler treats a missing `InitialValue` as `nil`. With `omitempty`, when marshaling a spec with `InitialValue: nil`, the field will be omitted from JSON output. This is acceptable behavior but worth noting that round-tripping a spec with explicit `nil` will lose the distinction between "unset" and "explicitly nil".

**Fix**: This is acceptable as-is since both cases result in `nil` accumulator. No change required.

---

### Finding #3 — NIT: Test helper `copyMap` is shallow copy only

**Location**: Test helpers section

**Problem**: The `copyMap` helper performs a shallow copy:
```go
func copyMap(m map[string]any) map[string]any {
    out := make(map[string]any, len(m))
    for k, v := range m {
        out[k] = v
    }
    return out
}
```

This is sufficient for test assertions capturing what was passed to the evaluator, but nested maps/slices will still share references. For the test cases described, this is acceptable.

**Fix**: No change required. The shallow copy is sufficient for verifying input shapes.

---

## Summary

| Severity | Count |
|----------|-------|
| HIGH     | 0     |
| MEDIUM   | 0     |
| NIT      | 3     |

---

## Verdict

**APPROVED**

The design document is thorough and implementation-ready:

1. **Pattern compliance**: The map/reduce handlers exactly mirror the verified filter/find pattern in validation order, source resolution, budget guards, ZEN invocation via `projectItemInputs`, and result storage.

2. **Struct definitions**: MapSpec and ReduceSpec follow the established FilterSpec/FindSpec conventions with proper JSON tags and required fields.

3. **Validation coverage**: Both handlers validate maxItems > 0 (no defaulting), over, jdmId, saveAs, and dep.Decide presence — matching the existing handlers exactly.

4. **Test matrix**: Covers all required scenarios:
   - Empty array (no ZEN calls, stores empty/initialValue)
   - Single/multiple items
   - maxItems exceeded (validation error before iteration)
   - Invalid source (not found, not array)
   - Context cancellation
   - ZEN errors
   - Specific contract tests (transform, sum, max, build-object)
   - Index as float64 verification for reduce

5. **Edge cases documented**: Nil items, nested paths, non-map items with projection, ZEN returns non-map/nil.

6. **Reduce contract**: The `{accumulator, current, index}` shape is clear, with `current` using `projectItemInputs` for consistency and `index` as `float64` for JSON compatibility.

The design can proceed to implementation without blocking changes.
