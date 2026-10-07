# Implementation Plan: Flow Input Validation with JSON Schema

This plan implements input validation for flows using JSON Schema (github.com/santhosh-tekuri/jsonschema/v6). Flows declare a schema in their trigger spec to validate incoming request params, query, headers, and body.

## Design Decisions

1. **Schema field type:** `json.RawMessage` in `TriggerInput` to hold the JSON Schema verbatim. This keeps the Go struct simple and delegates schema complexity to the JSON Schema library.

2. **Extraction model:** Flatten all input sources (params, query, headers, body) into a single `map[string]any` before validation. The schema validates this unified map.

3. **Schema caching:** Compile schemas at flow load time and cache in the `cachedFlow` struct (`config/codec.go`). This avoids per-request compilation overhead. The cache wire format gains a `CompiledSchema` field that is transient (not serialized) — it is rebuilt from `Tree` on decode.

4. **Error response shape:** Return a structured 400 response with field paths and messages derived from JSON Schema validation errors. This provides actionable feedback to API consumers.

5. **Backward compatibility:** Flows without a `schema` field work exactly as before — extraction runs but validation is skipped.

---

## Implementation Plan

- [ ] 1. Add dependency `github.com/santhosh-tekuri/jsonschema/v6` to engine/go.mod.
      Run `go get github.com/santhosh-tekuri/jsonschema/v6@latest` in the engine directory.
      Files: engine/go.mod, engine/go.sum
      Verify: `cd engine && go mod tidy && go build ./...` succeeds.

- [ ] 2. Add `Schema json.RawMessage` field to `TriggerInput` in spec.go.
      The field is optional (omitempty). When present, it holds the JSON Schema document.
      Files: engine/internal/flow/spec.go (modify TriggerInput at line 41-46)
      Verify: `cd engine && go build ./...` — existing flows without schema still compile.

- [ ] 3. Create `engine/internal/flow/input.go` with extraction and validation logic.
      Implement three functions:
      - `ExtractInput(r *http.Request, pathParams map[string]string, spec TriggerInput) (map[string]any, error)`
        Extracts params (from pathParams), query (from r.URL.Query() for names in spec.Query), headers (from r.Header for names in spec.Headers, case-insensitive), and body (when spec.Body is true, parse r.Body as JSON). Returns a flat map.
      - `ValidateInput(input map[string]any, schema *jsonschema.Schema) error`
        Validates the input map against a pre-compiled JSON Schema. Returns an InputValidationError with field paths and messages on failure.
      - `CompileSchema(raw json.RawMessage) (*jsonschema.Schema, error)`
        Compiles a JSON Schema document, returning ClassValidation error on invalid schema.
      - `ExtractAndValidate(r *http.Request, pathParams map[string]string, spec TriggerInput, compiled *jsonschema.Schema) (map[string]any, error)`
        Combines extraction and validation. When compiled is nil (no schema), skip validation.
      Define `InputValidationError` struct with Field, Code, Message for structured errors.
      Files: engine/internal/flow/input.go (new file)
      Verify: `cd engine && go build ./...` — file compiles without errors.

- [ ] 4. Add tests for extraction and validation in `engine/internal/flow/input_test.go`.
      Table-driven tests covering:
      - Path params extraction only (no query/headers/body)
      - Query param extraction (single and multiple values)
      - Header extraction (case-insensitive: X-Tenant-Id, x-tenant-id)
      - Body extraction (when spec.Body=true, parse JSON body)
      - Body not extracted when spec.Body=false or body is empty
      - Required field missing → validation error with field path
      - Type mismatch (expected string, got number) → validation error
      - Pattern fail (regex mismatch) → validation error
      - Enum fail (value not in enum) → validation error
      - Nested body field validation
      - Schema compilation error (invalid schema document)
      - No schema (nil) → validation skipped, extraction succeeds
      Files: engine/internal/flow/input_test.go (new file)
      Verify: `cd engine && go test ./internal/flow/... -run TestInput` — all new tests pass.

- [ ] 5. Update `ValidateTree` in validate.go to verify schema compiles at publish time.
      In the `TypeTrigger` case (around line 90-95), after parsing TriggerSpec, add a check:
      if `spec.Input.Schema` is non-empty, call `CompileSchema(spec.Input.Schema)`. If it returns an error, add a validation issue with code "bad_input_schema" and the error message.
      Files: engine/internal/flow/validate.go (modify around line 90-95, in the TypeTrigger case)
      Verify: `cd engine && go test ./internal/flow/... -run TestValidateTree` — existing tests pass, add a test for bad schema.

- [ ] 6. Add schema compilation caching in config/codec.go.
      Modify `cachedFlow` struct (line 24-31) to add a transient `CompiledInputSchema *jsonschema.Schema` field (not serialized — it has no json tag).
      Modify `decodeFlow` (line 59-72) to compile the input schema from the tree's trigger spec after decoding. If the trigger spec has Input.Schema, call `flow.CompileSchema` and store in the returned FlowVersion. Use a helper to extract the schema from the tree root.
      Modify `FlowVersion` struct in store.go (around line 68) to add `CompiledInputSchema *jsonschema.Schema` (no json tag).
      Files: engine/internal/config/codec.go, engine/internal/config/store.go
      Verify: `cd engine && go test ./internal/config/... -run TestFlowEncodeDecode` — round-trip tests pass.

- [ ] 7. Wire extraction and validation into genericFlowHandler in server.go.
      Replace lines 203-206 (the current path-param-only extraction):
      ```go
      input := make(map[string]any, len(params))
      for name, val := range params {
          input[name] = val
      }
      ```
      With a call to `flow.ExtractAndValidate(r, params, triggerSpec.Input, fv.CompiledInputSchema)`.
      To get the triggerSpec, parse the root node's spec: `var triggerSpec flow.TriggerSpec; json.Unmarshal(fv.Tree.Spec, &triggerSpec)`.
      On validation error, check if it's an `*flow.InputValidationError` or multiple errors, and return a 400 with structured error body:
      ```go
      if err != nil {
          writeValidationError(w, err)  // new helper
          return
      }
      ```
      Add a `writeValidationError(w http.ResponseWriter, err error)` helper that extracts field/code/message from the error(s) and writes a JSON response like:
      `{"error": "invalid request", "details": [{"field": "...", "code": "...", "message": "..."}]}`
      The error is classified as ClassValidation so `statusForFlow` already maps it to 400.
      Files: engine/internal/httpapi/server.go (modify genericFlowHandler around lines 203-228)
      Verify: `cd engine && go test ./internal/httpapi/... -run TestHandler` — existing handler tests pass.

- [ ] 8. Add handler test for validation error response in server_test.go.
      Add a test that:
      - Creates a flow with a JSON Schema requiring a field (e.g., `{"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}}`)
      - Sends a request missing the required field
      - Asserts 400 response with structured error body containing field path and "required" code
      Files: engine/internal/httpapi/server_test.go
      Verify: `cd engine && go test ./internal/httpapi/... -run TestValidationError` — new test passes.

- [ ] 9. Update CMS types in cms/types/engine.ts.
      Add `schema?: unknown` to the TriggerInput interface. Since the CMS types file doesn't currently have TriggerInput defined directly (TriggerSpec is part of EngineNode.spec), add a comment or interface:
      ```typescript
      /** TriggerInput declares input extraction and validation. */
      export interface TriggerInput {
        params?: string[];
        query?: string[];
        headers?: string[];
        body?: boolean;
        schema?: unknown;  // JSON Schema for input validation
      }
      ```
      Files: cms/types/engine.ts (add near line 23, before EngineNode)
      Verify: `cd cms && npm run typecheck` (or equivalent) — type check passes.

- [ ] 10. Run full verification suite.
      Verify the complete implementation:
      - `cd engine && go build ./...` — builds successfully
      - `cd engine && go test ./internal/flow/... ./internal/httpapi/... ./internal/config/...` — all tests pass
      - Manual test: create a flow with a schema, send invalid request, verify 400 with details
      Files: (none — verification only)
      Verify: All commands above succeed.

---

## Verification Commands (run after all steps)

```bash
cd /home/nuzirwan/project/rule-engine-api/engine
go build ./...
go test ./internal/flow/... ./internal/httpapi/... ./internal/config/...
```

## Key Files Summary

| File | Change |
|------|--------|
| engine/go.mod | Add jsonschema/v6 dependency |
| engine/internal/flow/spec.go | Add Schema field to TriggerInput |
| engine/internal/flow/input.go | NEW: extraction and validation logic |
| engine/internal/flow/input_test.go | NEW: table-driven tests |
| engine/internal/flow/validate.go | Compile schema at publish time |
| engine/internal/config/codec.go | Cache compiled schema on decode |
| engine/internal/config/store.go | Add CompiledInputSchema to FlowVersion |
| engine/internal/httpapi/server.go | Wire ExtractAndValidate, structured error response |
| engine/internal/httpapi/server_test.go | Add validation error test |
| cms/types/engine.ts | Add TriggerInput interface with schema field |

## Notes

- The `jsonschema/v6` library validates JSON data against a JSON Schema draft (2020-12 by default). It returns detailed validation errors with instance paths.
- Headers are case-insensitive per HTTP spec — use `r.Header.Get(name)` which handles canonicalization.
- Query params may be multi-valued; for simplicity, take the first value (`r.URL.Query().Get(name)`). If arrays are needed later, the schema can declare `{"type": "array"}` and extraction can be extended.
- Body is only parsed when `spec.Body == true` AND the request has a non-empty body AND Content-Type is application/json.
