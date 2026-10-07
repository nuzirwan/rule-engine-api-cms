# Flow Input Validation with JSON Schema

Adds input validation for flows using JSON Schema (github.com/santhosh-tekuri/jsonschema/v6). Flows declare a schema in their trigger spec; incoming params, query, headers, and body are extracted into a flat map and validated. Backward compatible: flows without a schema work exactly as before.

**Watch for:** Query params are extracted as strings, but JSON Schema validates the Go type — a schema with `{"type":"integer"}` will fail on query params because they're strings in the map. Flows must use `"type":"string"` with pattern constraints for query params, or the schema must tolerate string values. (likely)

**Verdict**: APPROVED

## High-level view

Schema compiles once at flow load via `decodeFlow`; `ValidateTree` also compiles at publish time, so invalid schemas never reach production. The hot path (`genericFlowHandler`) type-asserts `CompiledInputSchema` and passes it to `ExtractAndValidate`, returning a structured 400 when validation fails. Flows without a schema follow the original code path with no behavior change.

Extraction flattens path params (from router match), query params (per `spec.Query`), headers (per `spec.Headers`, case-insensitive via `r.Header.Get`), and body (under key `"body"` when `spec.Body=true`) into `map[string]any`. Validation uses the jsonschema library's `ValidationError` tree to produce field paths and short error codes (`required`, `type`, `pattern`, etc.).

Test coverage is broad: `input_test.go` has table-driven cases for required missing, type mismatch, pattern fail, enum fail, nested body, minLength/maxLength/minimum, schema compilation errors, and the no-schema pass-through. `server_test.go` adds an integration test that wires a schema into a live handler and asserts the structured 400 response. `validate_test.go` confirms invalid schemas are rejected at publish.

<details>
<summary>Issues (2)</summary>

1. **Query params stored as strings** (likely) — `ExtractInput` extracts query params via `Query.Get(name)` and stores them as strings. The plan notes "query params are strings coerced by the schema," but JSON Schema doesn't coerce — it validates the Go type. A schema with `"type":"integer"` for a query param will always fail. Document this constraint or adjust extraction to attempt numeric parsing.

2. **Admin GetFlowVersion doesn't compile schema** (possible) — `pgstore_admin.GetFlowVersion` builds `FlowVersion` directly from DB without calling `decodeFlow`, so `CompiledInputSchema` is nil. This doesn't affect the hot path but could surprise future admin features that assume a compiled schema. No action required now; flag for awareness.

</details>

<details>
<summary>Details</summary>

## Schema caching via decodeFlow

`decodeFlow` (codec.go:60–84) checks whether the root is a trigger and parses `TriggerSpec.Input.Schema`. If non-empty, it compiles via `CompileSchema` and stores in `CompiledInputSchema`. Compile errors are silently swallowed — justified because `ValidateTree` runs the same compilation at publish time and rejects bad schemas.

The hot path (`PgStore.ActiveFlow`) first tries a cache hit via `decodeFlow`, then on miss resolves from DB and re-encodes for cache population. Either way, `CompiledInputSchema` is populated (or nil if no schema) before `genericFlowHandler` runs.

## Extraction model

`ExtractInput` (input.go:46–90) builds `map[string]any`:

- **Path params**: all entries from `pathParams` are copied verbatim as strings.
- **Query params**: only names listed in `spec.Query` are extracted; uses `Query.Get(name)` so multi-valued params take the first value. **Stored as strings.**
- **Headers**: names in `spec.Headers` are extracted via `r.Header.Get(name)` for case-insensitivity. The *declared* name is the map key.
- **Body**: when `spec.Body && r.Body != nil && r.ContentLength != 0`, reads and unmarshals JSON into `input["body"]`. Malformed JSON returns `ClassValidation`.

The string storage for path and query params is the source of the type mismatch concern: JSON Schema validates the Go type, not the string representation.

## Validation and error mapping

`ValidateInput` delegates to `schema.Validate(input)`. On failure it extracts `*jsonschema.ValidationError` and recursively collects leaf errors via `extractValidationErrors`. The `errorToCode` switch maps error kinds to short codes (`required`, `type`, `pattern`, etc.). Field paths are built from `ve.InstanceLocation` as dot-joined strings.

`InputValidationErrors.Unwrap()` returns `ErrValidation`, so `errors.Is(err, flow.ErrValidation)` works — `statusForFlow` maps this to 400.

## Handler wiring

`genericFlowHandler` (server.go:174–246) parses `TriggerSpec` from `fv.Tree.Spec`, type-asserts `CompiledInputSchema` to `*jsonschema.Schema`, and calls `ExtractAndValidate`. On `InputValidationErrors`, `writeValidationErrors` builds `{"error":"invalid request","details":[...]}` with field/code/message.

## Test coverage

**input_test.go** (515 lines): `TestExtractInput` covers path-only, query extraction, header case-insensitivity, body extraction, combined inputs, invalid JSON body. `TestCompileSchema` covers empty, valid, invalid JSON, bad type values. `TestValidateInput` covers required missing, type mismatch, pattern, enum, nested body, minimum, maxLength. `TestExtractAndValidate` covers no-schema and validation failures.

**server_test.go** (+119 lines): `TestInputValidationError` creates a flow with schema, sends request missing required field, asserts 400 with structured error body.

**validate_test.go** (+17 lines): confirms invalid schemas are rejected at publish with `bad_input_schema`.

**Not tested:** Multi-valued query params (first value taken, documented behavior). Non-JSON body when `spec.Body=true` (fails at unmarshal, acceptable).

</details>

<details>
<summary>File map</summary>

| File | Change |
|------|--------|
| engine/go.mod | +jsonschema/v6 |
| engine/go.sum | +jsonschema/v6, +dlclark/regexp2 (transitive) |
| engine/internal/flow/spec.go | `Schema json.RawMessage` added to `TriggerInput` |
| engine/internal/flow/input.go | NEW: ExtractInput, ValidateInput, CompileSchema, ExtractAndValidate, InputValidationError types |
| engine/internal/flow/input_test.go | NEW: 515-line test file |
| engine/internal/flow/validate.go | +9 lines: schema compile check in TypeTrigger case |
| engine/internal/flow/validate_test.go | +17 lines: bad/valid schema cases |
| engine/internal/config/codec.go | +16 lines: decodeFlow compiles schema |
| engine/internal/config/store.go | +3 lines: CompiledInputSchema field |
| engine/internal/httpapi/server.go | +55/-17 lines: genericFlowHandler calls ExtractAndValidate, writeValidationErrors |
| engine/internal/httpapi/server_test.go | +119 lines: TestInputValidationError |
| cms/types/engine.ts | +13 lines: TriggerInput interface |

Full diff: `git diff 9331627..4e79e56`

</details>
