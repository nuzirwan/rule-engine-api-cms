# Implementation Plan: Dynamic Grouped Flow Workers — Phase 1

**Scope:** Data model, group CRUD, CMS content types. No gateway mode, no K8s workers. `dispatch.mode` stays `inline`.

## Features

- **FEAT-001**: Go Engine Group Data Model (types, validation)
- **FEAT-002**: Database Migration (all tables, seeded default group)
- **FEAT-003**: Admin API Handlers (group CRUD, version endpoint)
- **FEAT-004**: Flow Store Updates (FlowVersion.Group, flow_group_audit)
- **FEAT-005**: CMS Content Types (group schema, flow relation)

Features are ordered by dependency: types → schema → store → API → CMS.

---

## FEAT-001: Go Engine Group Data Model

- [ ] 1. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/config/group.go` with:
      - `ScalingMode` type and constants: `ScalingStatic`, `ScalingDynamic`, `ScalingEphemeral`
      - `Resources` struct: CPURequest, CPULimit, MemoryRequest, MemoryLimit strings
      - `ScalingConfig` struct: Mode, MinReplicas, MaxReplicas, ScaleDownDelay, StartupTimeout, Resources
      - `Group` struct: ID, Name, Description, Version, Connections []string, Scaling, Enabled, CreatedAt, UpdatedAt
      - `GroupAuditData` struct: Group *Group, Connections []string (for denormalized audit)
      - `GroupSummary` struct: ID, Name, Enabled, Version, UpdatedAt
      - `ValidateGroup(g *Group) error` function: ID regex `^[a-z][a-z0-9-]*$`, name required, replica constraints, static mode requires minReplicas>0, Mode validation (grill #8)
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/config/group.go`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go build ./...`

---

## FEAT-002: Database Migration

- [ ] 2. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/migrations/0004_groups.up.sql` with:
      - `CREATE TABLE groups` with version column for hot-reload, scaling fields, resource limits, enabled flag, timestamps, CHECK constraints
      - `CREATE TABLE group_connections` junction table (not JSONB array) with FK to groups ON DELETE CASCADE
      - `CREATE TABLE group_audit` for rollback support with old_data/new_data JSONB, operation CHECK
      - `CREATE TABLE group_connections_audit` for junction table rollback
      - `CREATE TABLE flow_group_audit` for flow assignment tracking
      - `ALTER TABLE flow_versions ADD COLUMN group_id` with FK to groups ON DELETE RESTRICT
      - `INSERT INTO groups` to seed 'default' group
      - All required indexes
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/migrations/0004_groups.up.sql`
      Verify: SQL syntax valid (build passes with embed)

- [ ] 3. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/migrations/0004_groups.down.sql` that reverses all changes in correct order.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/migrations/0004_groups.down.sql`
      Verify: SQL syntax valid

- [ ] 4. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/migrations/migrations.go` to embed the new migration files.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/migrations/migrations.go`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go build ./...`

---

## FEAT-003: Admin API Handlers

- [ ] 5. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/config/pgstore_group.go` with GroupStore methods:
      - `GetGroup(ctx, env, groupID) (Group, error)` — query groups + group_connections
      - `GetGroupVersion(ctx, env, groupID) (int, error)` — lightweight version query for hot-reload polling
      - `ListGroups(ctx, env) ([]GroupSummary, error)` — ordered list
      - `UpsertGroup(ctx, env, group, changedBy, reason) error` — validate first, capture pre-mutation state BEFORE update (critical for rollback), upsert with version++, replace connections, audit
      - `DeleteGroup(ctx, env, groupID, changedBy, reason) error` — reject 'default', reject if flows assigned (409), capture state, delete, audit
      - `replaceGroupConnections(ctx, tx, groupID, connections, changedBy, reason) error` — delete old, insert new, INCREMENT groups.version (grill #2), audit
      - `getGroupAuditData(ctx, tx, groupID) (json.RawMessage, error)` — fetch full state for audit
      - `CountFlowsByGroup(ctx, env, groupID) (int, error)` — count active flows in group
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/config/pgstore_group.go`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go build ./...`

- [ ] 6. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/httpapi/admin_groups.go` with handlers:
      - `HandlePutGroup` — decode, validate with config.ValidateGroup, call store.UpsertGroup
      - `HandleGetGroup` — r.PathValue("id"), store.GetGroup
      - `HandleListGroups` — store.ListGroups
      - `HandleDeleteGroup` — check flows assigned (409), check default (403), store.DeleteGroup
      - `HandleGetGroupVersion` — store.GetGroupVersion, return `{version: N}`
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/httpapi/admin_groups.go`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go build ./...`

- [ ] 7. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/httpapi/admin.go` to register group routes:
      - `PUT /admin/groups/{id}` → HandlePutGroup
      - `GET /admin/groups/{id}` → HandleGetGroup
      - `GET /admin/groups` → HandleListGroups
      - `DELETE /admin/groups/{id}` → HandleDeleteGroup
      - `GET /internal/groups/{id}/version` → HandleGetGroupVersion
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/httpapi/admin.go`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go test ./internal/httpapi/...`

---

## FEAT-004: Flow Store Updates

- [ ] 8. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/config/store.go`: Add `Group string` field to `FlowVersion` struct with JSON tag `"group"`.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/config/store.go`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go build ./...`

- [ ] 9. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/config/pgstore.go`:
      - Modify `resolveFlowFromDB` to SELECT fv.group_id and populate FlowVersion.Group
      - Add `UpdateFlowGroup(ctx, env, flowID, groupID, changedBy, reason) error` — get current group_id, capture in flow_group_audit, UPDATE flow_versions SET group_id
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/config/pgstore.go`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go test ./internal/config/...`

- [ ] 10. Add PATCH /admin/flows/{id} handler for group assignment:
      - Add HandlePatchFlow to admin_groups.go or admin_handlers.go
      - Accept `{group: string}` body
      - Call store.UpdateFlowGroup
      - Wire route in admin.go
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/httpapi/admin_groups.go`, `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine/internal/httpapi/admin.go`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go test ./internal/httpapi/...`

---

## FEAT-005: CMS Content Types

- [ ] 11. Create CMS group content type directory structure and component:
      - Create directories under cms/src/api/group/ and cms/src/components/config/
      - Create `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/components/config/resource-limits.json` with cpuRequest, cpuLimit, memoryRequest, memoryLimit
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/components/config/resource-limits.json`
      Verify: File exists with valid JSON

- [ ] 12. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/group/content-types/group/schema.json` with:
      - collectionType, draftAndPublish: true
      - groupId (uid, required, unique, regex), name, description, enabled, scalingMode (enum), replicas, timeouts, resources component, connections relation (manyToMany to connection)
      - NOTE: No flows relation (intentional per design)
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/group/content-types/group/schema.json`
      Verify: Valid JSON

- [ ] 13. Create standard Strapi boilerplate files for group content type:
      - `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/group/controllers/group.ts`
      - `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/group/routes/group.ts`
      - `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/group/services/group.ts`
      Files: controllers/group.ts, routes/group.ts, services/group.ts
      Verify: Files exist

- [ ] 14. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/flow/content-types/flow/schema.json`: Add `group` attribute (manyToOne relation to api::group.group).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/flow/content-types/flow/schema.json`
      Verify: Valid JSON

- [ ] 15. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/connection/content-types/connection/schema.json`: Add `groups` attribute for inverse manyToMany relation.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms/src/api/connection/content-types/connection/schema.json`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms && npm run build`

---

## Verification Summary

After all steps:
1. `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go build ./...` — passes
2. `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/engine && go test ./internal/config/... ./internal/httpapi/...` — passes
3. `cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-grouped-workers-phase1/cms && npm run build` — passes
4. Migration SQL is syntactically valid
5. Default group is seeded and cannot be deleted

---

## Key Design Decisions (from design.md)

1. **Connections in junction table** — group_connections, NOT JSONB array
2. **Audit captures full state** — old_data/new_data include connections for rollback
3. **Version increments on connection changes** — for hot-reload detection (grill #2)
4. **ValidateGroup is shared** — Admin API and CMS transform use same function
5. **Flow→group via flow.group_id** — not via group.flows relation
6. **ON DELETE RESTRICT** — deleting a group fails if flows are assigned
7. **Default group is protected** — cannot be deleted (403)
8. **ScalingMode validation** — must be static|dynamic|ephemeral (grill #8)
9. **flow_group_audit** — tracks flow assignment changes (grill #9)
