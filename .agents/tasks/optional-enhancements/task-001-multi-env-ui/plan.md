# Implementation Plan: TASK-001 Multi-Environment UI

Build environment switching UI for the rule-engine CMS (Strapi 5) so operators can manage flows/JDMs/connections across multiple environments (dev, staging, prod) from a single CMS instance.

## Codebase Findings

### Existing Infrastructure
- **Environment content type** exists at `cms/src/api/environment/` with fields: `name` (uid, required), `adminApiBaseUrl`, `operatorTokenRef`, `payloadEnv` (default "")
- **Flow/JDM/Connection** content types all have `environment` relation (manyToOne to `api::environment.environment`)
- **AdminClient** in `cms/src/plugins/rule-engine/server/src/services/admin-client.ts` already accepts env config via `resolveAdminConfig()` — it resolves baseUrl/token from Environment relation or falls back to env vars
- **publish-core.ts** already uses `clientForEntry()` pattern to create AdminClient from a document's environment relation

### Component Patterns (to follow)
- **SyncPanel** (`admin/src/components/SyncPanel/index.tsx`): FetchState union type, useFetchClient, @strapi/design-system Table/Badge/Button
- **AuditViewer** (`admin/src/components/AuditViewer/index.tsx`): Same pattern, fetch-on-mount, error/loading/ok states
- **SyncPage** (`admin/src/pages/SyncPage.tsx`): Main + HeaderLayout + ContentLayout wrapper

### Test Patterns
- Server tests use vitest + nock for HTTP mocking
- Mock strapi.documents() factory pattern (see `sync.test.ts`)
- Admin smoke tests in `admin/src/smoke.test.ts` use vi.fn() mocks

## Implementation Plan

- [ ] 1. **Create EnvironmentContext and types**
      Create React context for managing selected environment with localStorage persistence.
      Files:
      - `cms/src/plugins/rule-engine/admin/src/contexts/types.ts` — Environment interface
      - `cms/src/plugins/rule-engine/admin/src/contexts/EnvironmentContext.tsx` — Provider + hook + localStorage
      Verify: `cd /home/nuzirwan/project/rule-engine-api/cms && npm run build` — TypeScript compiles without errors.

- [ ] 2. **Add server GET /environments route**
      Server route to list all Environment content type entries for the frontend.
      Files:
      - `cms/src/plugins/rule-engine/server/src/controllers/environment.ts` — new controller with list()
      - `cms/src/plugins/rule-engine/server/src/routes/index.ts` — add GET /environments route
      - `cms/src/plugins/rule-engine/server/src/index.ts` — register environment controller
      Verify: `npm run build && npm run test` — builds and existing tests pass.

- [ ] 3. **Create EnvironmentContext unit tests**
      Test the context provider, hook, and localStorage behavior.
      Files:
      - `cms/src/plugins/rule-engine/admin/src/contexts/EnvironmentContext.test.tsx`
      Verify: `npm run test` — new tests pass.

- [ ] 4. **Create environment controller unit tests**
      Test the server-side environment list endpoint.
      Files:
      - `cms/src/plugins/rule-engine/server/tests/environment.test.ts`
      Verify: `npm run test` — new tests pass.

- [ ] 5. **Create EnvironmentSelector component**
      Dropdown component using @strapi/design-system SingleSelect that consumes EnvironmentContext.
      Files:
      - `cms/src/plugins/rule-engine/admin/src/components/EnvironmentSelector/index.tsx`
      Verify: `npm run build` — compiles.

- [ ] 6. **Create EnvironmentManager component**
      Full CRUD panel for environments with list, add/edit modal, delete, test connection.
      Files:
      - `cms/src/plugins/rule-engine/admin/src/components/EnvironmentManager/index.tsx`
      - `cms/src/plugins/rule-engine/admin/src/components/EnvironmentManager/types.ts`
      Verify: `npm run build` — compiles.

- [ ] 7. **Add environment CRUD server routes**
      POST/PUT/DELETE routes for environment management, plus POST /:id/test for connection testing.
      Files:
      - `cms/src/plugins/rule-engine/server/src/controllers/environment.ts` — add create, update, remove, testConnection
      - `cms/src/plugins/rule-engine/server/src/routes/index.ts` — add CRUD routes
      Verify: `npm run build && npm run test` — builds and tests pass.

- [ ] 8. **Create EnvironmentsPage**
      Page wrapper for EnvironmentManager following SyncPage pattern.
      Files:
      - `cms/src/plugins/rule-engine/admin/src/pages/EnvironmentsPage.tsx`
      Verify: `npm run build` — compiles.

- [ ] 9. **Integrate EnvironmentProvider into admin entry**
      Wrap admin plugin with EnvironmentProvider and register EnvironmentsPage route.
      Files:
      - `cms/src/plugins/rule-engine/admin/src/index.tsx` — add provider wrapping and page registration
      Verify: `npm run build` — compiles.

- [ ] 10. **Add unit tests for EnvironmentSelector and EnvironmentManager**
       Test component rendering and interactions.
       Files:
       - `cms/src/plugins/rule-engine/admin/src/components/EnvironmentSelector/EnvironmentSelector.test.tsx`
       - `cms/src/plugins/rule-engine/admin/src/components/EnvironmentManager/EnvironmentManager.test.tsx`
       Verify: `npm run test` — all tests pass.

- [ ] 11. **Make SyncPanel environment-aware**
       Filter sync status by selected environment, show env badges when in "All" mode.
       Files:
       - `cms/src/plugins/rule-engine/admin/src/components/SyncPanel/index.tsx` — add env filtering
       - `cms/src/plugins/rule-engine/server/src/controllers/sync.ts` — add optional env query param
       Verify: `npm run build && npm run test` — builds and existing sync tests pass.

- [ ] 12. **Add env-filtered sync tests**
       Test sync controller with environment filter.
       Files:
       - `cms/src/plugins/rule-engine/server/tests/sync.test.ts` — add env filter test cases
       Verify: `npm run test` — all tests pass.

- [ ] 13. **Final verification and cleanup**
       Run full build and test suite, verify no regressions.
       Verify: `cd /home/nuzirwan/project/rule-engine-api/cms && npm run build && npm run test` — all pass.

## Key Design Decisions

1. **EnvironmentContext shape**: Provider fetches environments on mount, stores selected env name in localStorage, exposes `{ environments, selectedEnv, selectEnv, isLoading, error }` via hook.

2. **Default environment**: When no environment is selected or localStorage is empty, `selectedEnv` is `null` meaning "default/prod" (uses env var fallback in AdminClient). UI shows this as "Default" option.

3. **Color-coded badges**: Environment names containing "prod" get success variant, "staging" gets alternative, "dev" gets warning, others get neutral.

4. **Test Connection**: Calls the environment's engine at GET /livez (or /readyz if livez doesn't exist) to verify reachability. Shows toast on success/failure.

5. **Strapi 5 integration**: Page registration uses app.createSettingSection (for settings pages) or direct menu injection. Provider wraps at plugin scope, not globally.

6. **Server route prefix**: All routes under `/rule-engine/` (existing pattern).

## Files Summary

### New Files (Admin)
- `cms/src/plugins/rule-engine/admin/src/contexts/types.ts`
- `cms/src/plugins/rule-engine/admin/src/contexts/EnvironmentContext.tsx`
- `cms/src/plugins/rule-engine/admin/src/contexts/EnvironmentContext.test.tsx`
- `cms/src/plugins/rule-engine/admin/src/components/EnvironmentSelector/index.tsx`
- `cms/src/plugins/rule-engine/admin/src/components/EnvironmentSelector/EnvironmentSelector.test.tsx`
- `cms/src/plugins/rule-engine/admin/src/components/EnvironmentManager/index.tsx`
- `cms/src/plugins/rule-engine/admin/src/components/EnvironmentManager/types.ts`
- `cms/src/plugins/rule-engine/admin/src/components/EnvironmentManager/EnvironmentManager.test.tsx`
- `cms/src/plugins/rule-engine/admin/src/pages/EnvironmentsPage.tsx`

### New Files (Server)
- `cms/src/plugins/rule-engine/server/src/controllers/environment.ts`
- `cms/src/plugins/rule-engine/server/tests/environment.test.ts`

### Modified Files
- `cms/src/plugins/rule-engine/admin/src/index.tsx` — provider + page registration
- `cms/src/plugins/rule-engine/server/src/index.ts` — register environment controller
- `cms/src/plugins/rule-engine/server/src/routes/index.ts` — add environment routes
- `cms/src/plugins/rule-engine/admin/src/components/SyncPanel/index.tsx` — env filtering
- `cms/src/plugins/rule-engine/server/src/controllers/sync.ts` — env query param
- `cms/src/plugins/rule-engine/server/tests/sync.test.ts` — env filter tests

## Verification Commands

```bash
cd /home/nuzirwan/project/rule-engine-api/cms && npm run build && npm run test
```

Both must pass. The build verifies TypeScript compilation and Strapi build. The test suite runs vitest covering all server and admin tests.
