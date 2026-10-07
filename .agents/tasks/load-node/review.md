# Load node replaces JSON file connector

The change removes the `json-file` connector from the driver registry and introduces a `load` node type that loads JSON data mid-flow from three mutually-exclusive sources: inline data, local files, or HTTP URLs. The loaded data (optionally filtered via JSONPath) is stored in `Ctx.Data[saveAs]` for use by subsequent collection nodes. Watch for: the implementation meets all spec requirements and tests cover edge cases well; no blocking concerns.

**Verdict**: APPROVED

## High-level view

The connector-to-node migration is complete and clean. The `json-file` driver (440 lines) and its tests (697 lines) are deleted, replaced by 228 lines in `control_handlers.go` and 384 lines of tests. The new implementation follows the existing handler pattern (filterHandler, mapHandler) and stores results in the same location (`Ctx.Data`).

Source mode validation correctly enforces exactly one of `data`, `path`, or `url` by counting non-zero fields and returning validation errors for zero or multiple sources. The 30-second HTTP timeout is implemented via a shared client with connection pooling, which is production-appropriate.

The documentation is comprehensive. `load-node.md` covers all three modes, JSONPath extraction, integration with collection nodes, error handling, and best practices. The `connectors.md` file has the json-file section removed and the type table updated.

Test coverage is thorough across the test matrix: inline data (object, array, nested JSONPath), local files (valid, not found, invalid JSON), URLs (valid, 404, 500, invalid JSON, invalid scheme), validation errors (missing saveAs, no source, multiple sources), and context cancellation. An integration test verifies load → filter chaining.

<details>
<summary>Issues (0)</summary>

No blocking issues identified.

</details>

<details>
<summary>Details</summary>

## Handler implementation

The `loadHandler` follows the existing pattern: parse spec, validate, execute, store in `Ctx.Data[saveAs]`. Source counting enforces the exactly-one-source constraint:

```go
sources := 0
if spec.Data != nil { sources++ }
if spec.Path != "" { sources++ }
if spec.URL != "" { sources++ }
if sources == 0 { return ..., validationf("requires one of data, path, or url") }
if sources > 1 { return ..., validationf("requires exactly one of data, path, or url") }
```

For inline data with JSONPath, the code marshals to bytes then uses gjson — necessary because gjson operates on bytes, not parsed values.

## Error classification

- `ErrValidation` for user errors (missing saveAs, no source, multiple sources, file not found, invalid JSON, bad URL scheme)
- `ErrUpstream` for external failures (HTTP errors, network issues)
- `ErrTimeout` for context cancellation

## Unrelated changes

The commit bundles CMS plugin changes (`index.tsx`, `smoke.test.ts`) for Strapi 5 router API migration. These don't affect load node functionality but shouldn't be in this commit.

</details>

<details>
<summary>Files changed (12)</summary>

| File | Change |
|------|--------|
| cms/src/plugins/rule-engine/admin/src/index.tsx | M (unrelated: Strapi 5 router migration) |
| cms/src/plugins/rule-engine/admin/src/smoke.test.ts | M (unrelated: test update) |
| docs/examples/connectors.md | M (removed json-file section) |
| docs/examples/load-node.md | A (new documentation) |
| engine/internal/connect/drivers/jsonfile.go | D (removed) |
| engine/internal/connect/drivers/jsonfile_test.go | D (removed) |
| engine/internal/connect/drivers/registry.go | M (removed registration) |
| engine/internal/flow/control_handlers.go | M (added loadHandler) |
| engine/internal/flow/control_handlers_test.go | M (added load tests) |
| engine/internal/flow/interpreter.go | M (registered handler) |
| engine/internal/flow/node.go | M (added TypeLoad) |
| engine/internal/flow/spec.go | M (added LoadSpec) |

[Full diff: `git diff HEAD~1`]

</details>
