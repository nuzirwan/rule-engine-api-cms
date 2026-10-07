# Validate and Dry Run buttons for flow editor

Adds two testing capabilities to the flow canvas toolbar: a Validate button that sends the current canvas tree to the engine's validation endpoint and surfaces structural issues and fixture failures, and a Dry Run button that opens a modal for test payload input and displays the execution trace, response, and errors. The implementation spans three layers: the engine gains candidate-mode support on its dry-run endpoint (matching the existing validate pattern), the CMS server adds a validate controller with two proxy endpoints, and the admin UI wires buttons with loading states and feedback into the existing FlowCanvasField component.

**Watch for:** The validate controller comment block (lines 1-15) claims the engine's dry-run endpoint "only supports stored mode" — but the Go diff shows it now accepts candidate mode via the `flow` field. This stale comment is cosmetic; the actual implementation correctly uses candidate mode for both endpoints. **[confirmed]**

**Verdict**: APPROVED

## High-level view

The engine's `/admin/flows/dry-run` endpoint gains candidate mode: when the `flow` object is present, it uses the inline tree instead of fetching from storage. The mode-selection switch is clean — candidate wins if present, then flowId+version, then input.method+path lookup. Writes remain suppressed (AC-14).

The CMS controller layer adds `/rule-engine/validate` and `/rule-engine/dry-run` as proxies that wrap the engine's candidate-mode contracts. Input validation rejects missing tree/method/path/input fields with 400; engine 5xx surfaces as 503 with `recoverable: true`. The controller follows the existing error-classification pattern from other controllers.

The UI wires Validate and Dry Run into the sidebar below the import/export section. Validate is a single-click operation showing inline success/failure with structural issues and fixture failures listed. Dry Run opens a modal with a JSON textarea for input, displays the execution trace and response on completion, and shows errors inline. Both use Strapi design-system components (`Button`, `Modal`, `Textarea`) with loading states. No persistence occurs — the canvas tree is read from current state at invocation time.

<details>
<summary>Issues (1)</summary>

1. **Stale controller comment** — The comment at the top of `validate.ts` claims dry-run "only supports stored mode" and describes a workaround using mocks, but the implementation sends to the actual dry-run endpoint which now supports candidate mode. Update the comment to match reality.

</details>

<details>
<summary>Details</summary>

## Engine dry-run candidate mode

The dry-run endpoint's mode-selection logic (lines 52-84 of the Go diff) now handles three cases in order: `req.Flow != nil` extracts the inline tree, `req.FlowID` with positive version loads from storage, and `input.Method+Path` resolves the active flow. The candidate path sets `ver = 0` to indicate no stored version. The interpreter call passes the extracted tree and synthesized `flow.Version{FlowID, Version}` regardless of mode.

The error message was updated to reflect all three modes: "provide flow object, flowId+version, or input.method+input.path".

## CMS validate controller

`validate.ts` exports a controller factory that creates two handlers sharing a helper for AdminClient construction and error mapping. The `createClient` helper catches config resolution failures (missing env vars) and returns 503 early — consistent with the §5.5 table used by other controllers.

Both handlers perform explicit field presence checks before calling the engine. The `validate` handler requires `tree`, `method`, `path`; the `dryRun` handler additionally requires `input`. Missing fields yield 400 with an error message naming the missing field.

The `AdminClient` gains two new methods: `validateFlowCandidate` constructs `{ env, flow: { flowId, method, path, tree, fixtures? } }` and `dryRunFlowCandidate` constructs `{ env, flow: { ... }, input, mocks? }`. Both call the same endpoints as their stored-mode counterparts, differing only in body shape.

## UI integration

The FlowCanvasField component adds state for validate (`loading`, `result`, `error`) and dry-run (`loading`, `result`, `error`, plus `showDryRunModal` and `dryRunInput`). The `useFetchClient` hook from Strapi admin provides the `post` function that handles auth headers and base URL automatically.

`handleValidate` builds the current canvas graph from `nodes` and `edges`, calls `canvasToTree` to convert to the engine node format, and posts to `/rule-engine/validate`. If `canvasToTree` throws (no trigger root, disconnected nodes), the error is shown inline without hitting the server.

`handleDryRunOpen` opens the modal and clears prior state. `handleDryRunExecute` follows the same pattern — build tree, parse the JSON input, post to `/rule-engine/dry-run`. The modal displays the trace as a collapsible JSON block and the response in a pre-formatted area.

## Test coverage

The validate controller has 13 unit tests covering success paths, validation errors, missing required fields, engine 5xx handling, and missing config. Tests use nock to intercept HTTP calls and verify the request shape includes the `flow` object (candidate mode) and `env` field.

**Not tested:** The UI handlers themselves (integration/e2e) — these are React hooks calling `post` and updating state. The commit message indicates the CMS test suite (288 tests) passes, but those are unit tests for the server layer.

</details>

<details>
<summary>File map</summary>

| File | Change |
|------|--------|
| `FlowCanvasField/index.tsx` | +306 lines: Validate/DryRun state, handlers, UI section with buttons, modal for dry-run |
| `controllers/validate.ts` | +203 lines: New controller with `validate` and `dryRun` handlers |
| `server/src/index.ts` | Register validate controller |
| `routes/index.ts` | Add `/validate` and `/dry-run` routes |
| `services/admin-client.ts` | +40 lines: `validateFlowCandidate` and `dryRunFlowCandidate` methods |
| `server/tests/validate.test.ts` | +332 lines: 13 unit tests for the controller |
| `cms/types/engine.ts` | +43 lines: TypeScript types for candidate-mode requests/responses |
| `engine/internal/httpapi/admin_dryrun.go` | +44/-13 lines: Add candidate mode to dry-run endpoint |

Full diff: `git -C /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun diff mainline...feature/flow-validate-dryrun`

</details>
