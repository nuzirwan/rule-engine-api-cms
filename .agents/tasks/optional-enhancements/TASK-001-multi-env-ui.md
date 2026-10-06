# TASK-001: Multi-Environment UI

## Summary
Add environment switching UI to the CMS so operators can manage flows/JDMs/connections across multiple environments (dev, staging, prod) from a single CMS instance.

## Context
- The CMS already has an `Environment` content type with `name`, `adminApiBaseUrl`, `operatorTokenRef`
- Flows already have an `env` field (defaults to `""`)
- The Admin API accepts `env` as a top-level field in requests
- What's missing: UI to switch environments, show which env you're editing, deploy to specific env

## Requirements

### 1. Environment Selector (global)
- Add environment dropdown/selector to CMS admin header or sidebar
- Persist selected environment in localStorage
- Show current environment prominently (color-coded badge?)
- Default to `""` (empty string = default/prod environment)

### 2. Environment-Aware Content Lists
- Flow list should filter by selected environment
- Show environment badge on each flow card
- Allow "All Environments" view with env column

### 3. Environment-Aware Editing
- When creating/editing a flow, auto-set `env` to currently selected environment
- Show warning if editing a flow from a different environment
- Validation panel should validate against the correct environment's engine

### 4. Environment-Aware Publish
- Publish action should target the selected environment's engine URL
- Show confirmation: "Publish to [env-name] at [url]?"
- Use environment-specific `operatorTokenRef` for auth

### 5. Environment Management Page
- List all configured environments
- Add/edit/delete environments
- Test connection button (calls engine's `/livez`)
- Show which flows exist in each environment (via sync status)

## Files to Modify/Create

### CMS Admin (`cms/src/plugins/rule-engine/admin/`)
- `src/components/EnvironmentSelector/index.tsx` — global env selector component
- `src/contexts/EnvironmentContext.tsx` — React context for selected environment
- `src/pages/EnvironmentsPage.tsx` — environment management page
- `src/components/FlowList/index.tsx` — modify to filter by env
- `src/components/FlowEditor/index.tsx` — modify to use selected env

### CMS Server (`cms/src/plugins/rule-engine/server/`)
- `src/services/admin-client.ts` — modify to accept env parameter, resolve correct URL/token
- `src/controllers/flow.ts` — pass env through to admin client

## Acceptance Criteria
- [ ] Environment selector visible in CMS admin UI
- [ ] Selecting environment persists across page reloads
- [ ] Flow list filters by selected environment
- [ ] New flows get `env` field set to selected environment
- [ ] Publish targets correct engine URL based on environment
- [ ] Environment management page allows CRUD on environments
- [ ] "Test Connection" verifies engine reachability

## Testing
- Unit tests for EnvironmentContext
- Unit tests for env-aware AdminClient
- Manual test: create environments, switch between them, verify flows filter correctly
