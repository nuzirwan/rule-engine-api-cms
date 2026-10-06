# Implementation Plan: Engine-CMS Bidirectional Sync

This plan implements bidirectional pull/sync so the Strapi CMS can pull existing config from the Go engine. The work is split into two phases: Part 1 adds engine read endpoints, Part 2 adds the CMS import/sync feature.

All paths are under `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync`.

---

## Part 1: Engine Admin API Read Endpoints

### Summary of Existing Code

- **AdminStore interface** (admin.go:28-41): Already has `Connections`, `GetJDM`, `GetFlowVersion`. Need to add list methods.
- **listConnections handler** (admin_handlers.go:234-264): Pattern to follow — requireStore, call store, redact settings, writeJSON.
- **PgStore methods** (pgstore.go, pgstore_admin.go): `GetFlowVersion` exists in pgstore_admin.go; `Connections` pattern shows cache-aside + SQL queries.
- **Route registration** (admin.go:88-99): `mux.Handle("GET /admin/connections", h(a.listConnections))` pattern.
- **fakeAdminStore** (admin_test.go:19-77): Has fields for each store method return value.

---

- [ ] 1. Add summary types to `engine/internal/config/store.go`.
      Add `FlowSummary`, `VersionSummary`, `JDMSummary` structs after the existing `FlowVersion` struct (~line 44).
      ```go
      // FlowSummary is the list-view projection for GET /admin/flows.
      type FlowSummary struct {
          ID            string    `json:"id"`
          Method        string    `json:"method"`
          Path          string    `json:"path"`
          ActiveVersion *int      `json:"activeVersion,omitempty"`
          UpdatedAt     time.Time `json:"updatedAt"`
      }
      
      // VersionSummary is the list-view projection for GET /admin/flows/{id}/versions.
      type VersionSummary struct {
          Version   int       `json:"version"`
          Validated bool      `json:"validated"`
          CreatedAt time.Time `json:"createdAt"`
          CreatedBy string    `json:"createdBy"`
      }
      
      // JDMSummary is the list-view projection for GET /admin/jdms.
      type JDMSummary struct {
          ID        string    `json:"id"`
          UpdatedAt time.Time `json:"updatedAt"`
      }
      ```
      Files: engine/internal/config/store.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/engine && CGO_ENABLED=1 go build ./...`

- [ ] 2. Add PgStore list methods to `engine/internal/config/pgstore_admin.go`.
      Add four methods following the GetFlowVersion pattern: pool lookup, SQL query, classifyPg errors.
      - `ListFlows(ctx, env) ([]FlowSummary, error)` — joins flows with active_pointers for activeVersion
      - `ListFlowVersions(ctx, env, flowID) ([]VersionSummary, error)` — queries flow_versions table
      - `ListJDMs(ctx, env) ([]JDMSummary, error)` — joins jdms with active_pointers
      - `GetConnection(ctx, env, key) (connect.ConnectionDef, error)` — reads single active connection
      Files: engine/internal/config/pgstore_admin.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/engine && CGO_ENABLED=1 go build ./...`

- [ ] 3. Extend AdminStore interface in `engine/internal/httpapi/admin.go`.
      Add after line 40 (before the closing brace):
      ```go
      ListFlows(ctx context.Context, env string) ([]config.FlowSummary, error)
      ListFlowVersions(ctx context.Context, env, flowID string) ([]config.VersionSummary, error)
      ListJDMs(ctx context.Context, env string) ([]config.JDMSummary, error)
      GetConnection(ctx context.Context, env, key string) (connect.ConnectionDef, error)
      ```
      Files: engine/internal/httpapi/admin.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/engine && CGO_ENABLED=1 go build ./...`

- [ ] 4. Add handler methods to `engine/internal/httpapi/admin_handlers.go`.
      Add six handlers after the existing `listConnections` function (~line 264), following its pattern:
      - `listFlows` — calls store.ListFlows, returns `{"flows": [...]}`
      - `getFlow` — gets flowID from path, calls store.GetFlowVersion with active version, returns full flow
      - `listFlowVersions` — gets flowID from path, calls store.ListFlowVersions, returns `{"flowId": "...", "versions": [...]}`
      - `listJdms` — calls store.ListJDMs, returns `{"jdms": [...]}`
      - `getJdm` — gets id from path, calls store.GetJDM, returns `{"jdmId": "...", "version": n, "doc": {...}}`
      - `getConnection` — gets key from path, calls store.GetConnection, redacts settings with observ.NewRedactor().Scrub(), returns single connection object
      Files: engine/internal/httpapi/admin_handlers.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/engine && CGO_ENABLED=1 go build ./...`

- [ ] 5. Register routes in `engine/internal/httpapi/admin.go` mount().
      Add after line 97 (GET /admin/connections) inside mount():
      ```go
      mux.Handle("GET /admin/flows", h(a.listFlows))
      mux.Handle("GET /admin/flows/{id}", h(a.getFlow))
      mux.Handle("GET /admin/flows/{id}/versions", h(a.listFlowVersions))
      mux.Handle("GET /admin/jdms", h(a.listJdms))
      mux.Handle("GET /admin/jdms/{id}", h(a.getJdm))
      mux.Handle("GET /admin/connections/{key}", h(a.getConnection))
      ```
      Files: engine/internal/httpapi/admin.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/engine && CGO_ENABLED=1 go build ./...`

- [ ] 6. Extend fakeAdminStore and add unit tests in `engine/internal/httpapi/admin_test.go`.
      Add fields to fakeAdminStore struct: `flowSummaries`, `flowSummariesErr`, `versionSummaries`, `versionSummariesErr`, `jdmSummaries`, `jdmSummariesErr`, `connection`, `connectionErr`.
      Add method implementations satisfying the extended interface.
      Add tests: TestAdminListFlows, TestAdminGetFlow, TestAdminListFlowVersions, TestAdminListJdms, TestAdminGetJdm, TestAdminGetConnection.
      Files: engine/internal/httpapi/admin_test.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/engine && go test ./...`

---

## Part 2: CMS Import/Sync Feature

### Summary of Existing Code

- **AdminClient** (admin-client.ts:54-190): Class with typed methods, private request() helper, Bearer auth.
- **audit.ts controller** (audit.ts:1-64): Pattern — resolve config, create client, try/catch AdminApiError, set ctx.body/status.
- **routes/index.ts**: Array of route objects with method, path, handler, config.
- **AuditViewer component** (AuditViewer/index.tsx): useFetchClient hook, FetchState union, loading/error/ok states.
- **engine.ts types** (types/engine.ts): TypeScript interfaces for each engine response.

---

- [ ] 7. Add TypeScript interfaces to `cms/types/engine.ts`.
      Add after AuditTrailResponse (~line 214):
      ```typescript
      // GET /admin/flows
      export interface FlowSummary {
        id: string;
        method: string;
        path: string;
        activeVersion: number | null;
        updatedAt: string;
      }
      export interface ListFlowsResponse {
        flows: FlowSummary[];
      }
      
      // GET /admin/flows/{id}
      export interface GetFlowResponse {
        flowId: string;
        version: number;
        method: string;
        path: string;
        tree: unknown;
        fixtures: unknown[];
      }
      
      // GET /admin/flows/{id}/versions
      export interface VersionSummary {
        version: number;
        validated: boolean;
        createdAt: string;
        createdBy: string;
      }
      export interface ListFlowVersionsResponse {
        flowId: string;
        versions: VersionSummary[];
      }
      
      // GET /admin/jdms
      export interface JDMSummary {
        id: string;
        updatedAt: string;
      }
      export interface ListJdmsResponse {
        jdms: JDMSummary[];
      }
      
      // GET /admin/jdms/{id}
      export interface GetJdmResponse {
        jdmId: string;
        version: number;
        doc: unknown;
      }
      
      // GET /admin/connections/{key}
      export interface GetConnectionResponse {
        key: string;
        type: string;
        settings: Record<string, unknown>;
        secretRef: string | null;
        resilience: unknown;
      }
      ```
      Files: cms/types/engine.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build`

- [ ] 8. Extend AdminClient in `cms/src/plugins/rule-engine/server/src/services/admin-client.ts`.
      Add methods after audit() (~line 189):
      ```typescript
      /** GET /admin/flows — list all flows with active version info. */
      listFlows(): Promise<ListFlowsResponse> {
        return this.request<ListFlowsResponse>('GET', '/admin/flows');
      }
      
      /** GET /admin/flows/{id} — get full flow with tree and fixtures. */
      getFlow(id: string): Promise<GetFlowResponse> {
        return this.request<GetFlowResponse>('GET', `/admin/flows/${encodeURIComponent(id)}`);
      }
      
      /** GET /admin/flows/{id}/versions — list versions for a flow. */
      listFlowVersions(id: string): Promise<ListFlowVersionsResponse> {
        return this.request<ListFlowVersionsResponse>('GET', `/admin/flows/${encodeURIComponent(id)}/versions`);
      }
      
      /** GET /admin/jdms — list all JDMs. */
      listJdms(): Promise<ListJdmsResponse> {
        return this.request<ListJdmsResponse>('GET', '/admin/jdms');
      }
      
      /** GET /admin/jdms/{id} — get full JDM document. */
      getJdm(id: string): Promise<GetJdmResponse> {
        return this.request<GetJdmResponse>('GET', `/admin/jdms/${encodeURIComponent(id)}`);
      }
      
      /** GET /admin/connections/{key} — get single connection def. */
      getConnection(key: string): Promise<GetConnectionResponse> {
        return this.request<GetConnectionResponse>('GET', `/admin/connections/${encodeURIComponent(key)}`);
      }
      ```
      Add imports for new response types.
      Files: cms/src/plugins/rule-engine/server/src/services/admin-client.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build`

- [ ] 9. Create sync controller at `cms/src/plugins/rule-engine/server/src/controllers/sync.ts`.
      Follow audit.ts pattern. Implement:
      - `status(ctx)` — calls listFlows, listJdms, listConnections from engine; queries CMS documents; computes diff (synced/localOnly/engineOnly based on ID matching)
      - `importAll(ctx)` — pulls each engine-only item, creates CMS documents via strapi.documents().create()
      - `importOne(ctx)` — gets type/id from params, pulls single item, creates/updates CMS document
      Files: cms/src/plugins/rule-engine/server/src/controllers/sync.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build`

- [ ] 10. Register sync routes in `cms/src/plugins/rule-engine/server/src/routes/index.ts`.
      Add to the admin.routes array:
      ```typescript
      {
        method: 'GET',
        path: '/sync/status',
        handler: 'sync.status',
        config: { policies: [] },
      },
      {
        method: 'POST',
        path: '/sync/import',
        handler: 'sync.importAll',
        config: { policies: [] },
      },
      {
        method: 'POST',
        path: '/sync/import/:type/:id',
        handler: 'sync.importOne',
        config: { policies: [] },
      },
      ```
      Files: cms/src/plugins/rule-engine/server/src/routes/index.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build`

- [ ] 11. Register sync controller in plugin server entry `cms/src/plugins/rule-engine/server/src/index.ts`.
      Import syncController and add to controllers object alongside publish and audit.
      Files: cms/src/plugins/rule-engine/server/src/index.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build`

- [ ] 12. Create SyncPanel types at `cms/src/plugins/rule-engine/admin/src/components/SyncPanel/types.ts`.
      ```typescript
      export type SyncItemType = 'flow' | 'jdm' | 'connection';
      export type SyncItemStatus = 'synced' | 'localOnly' | 'engineOnly';
      
      export interface SyncItem {
        id: string;
        type: SyncItemType;
        status: SyncItemStatus;
        name?: string;
      }
      
      export interface SyncStatusResponse {
        flows: { synced: string[]; localOnly: string[]; engineOnly: string[] };
        jdms: { synced: string[]; localOnly: string[]; engineOnly: string[] };
        connections: { synced: string[]; localOnly: string[]; engineOnly: string[] };
      }
      
      export interface ImportResult {
        imported: { flows: number; jdms: number; connections: number };
      }
      ```
      Files: cms/src/plugins/rule-engine/admin/src/components/SyncPanel/types.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build`

- [ ] 13. Create SyncPanel component at `cms/src/plugins/rule-engine/admin/src/components/SyncPanel/index.tsx`.
      Follow AuditViewer pattern with @strapi/design-system. Features:
      - FetchState for loading/error/ok
      - Display sync status with color-coded badges (green=synced, yellow=localOnly, blue=engineOnly)
      - "Import All" button calling POST /sync/import
      - Per-item "Import" buttons for engine-only items
      - Success/error toast feedback
      Files: cms/src/plugins/rule-engine/admin/src/components/SyncPanel/index.tsx
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build`

- [ ] 14. Create SyncPage at `cms/src/plugins/rule-engine/admin/src/pages/SyncPage.tsx`.
      Minimal page wrapper that renders SyncPanel with page title.
      ```typescript
      import { Box, Typography } from '@strapi/design-system';
      import { SyncPanel } from '../components/SyncPanel';
      
      export const SyncPage = () => (
        <Box padding={8}>
          <Typography variant="alpha" marginBottom={6}>Engine Sync</Typography>
          <SyncPanel />
        </Box>
      );
      
      export default SyncPage;
      ```
      Files: cms/src/plugins/rule-engine/admin/src/pages/SyncPage.tsx
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build`

- [ ] 15. Add vitest tests at `cms/src/plugins/rule-engine/server/tests/sync.test.ts`.
      Follow audit-controller.test.ts pattern with nock mocks. Test:
      - syncStatus returns correct diff structure
      - importAll calls correct engine endpoints
      - importOne with type/id params
      - 5xx returns 503 recoverable:true
      - 4xx returns original status recoverable:false
      Files: cms/src/plugins/rule-engine/server/tests/sync.test.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run test`

---

## Final Verification

After all steps complete:
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/engine && CGO_ENABLED=1 go build ./... && go test ./...
cd /home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms && npm run build && npm run test
```
