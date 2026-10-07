# Strapi CMS Performance Investigation Report

## Summary

After reviewing the codebase, **no critical memory leaks or severe performance bugs were found** in the custom rule-engine plugin. The code follows React best practices (proper cleanup, memoization, useCallback). However, several **medium-priority optimizations** are recommended to reduce memory pressure and improve responsiveness, particularly around the heavy UI components (ReactFlow, GoRules JDM editor) and database query patterns.

The primary performance concern is **resource-intensive admin UI components** combined with Strapi's baseline memory overhead and potential for large JSON payloads in the `tree` and `doc` fields.

---

## Findings

### 1. Heavy Admin UI Components — **Medium Severity**

**Files:**
- `/cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
- `/cms/src/plugins/rule-engine/admin/src/components/JdmEditorField/index.tsx`

**Issue:** The FlowCanvasField and JdmEditorField embed @xyflow/react and @gorules/jdm-editor respectively — both are substantial React libraries that render complex canvas/graph UIs. While these are lazy-loaded via the custom field descriptors, once mounted they consume significant memory.

**Evidence (lines 1-30):**
```typescript
// FlowCanvasField mounts ReactFlow with:
// - Background, Controls, MiniMap components
// - Debounced reserialize (300ms) on every change
// - Multiple refs: layoutRef, lastEmittedRef, timer
```

**Good practices already in place:**
- Debounced updates (DEBOUNCE_MS = 300)
- React.useCallback for handlers
- React.useMemo for parsed values
- Cleanup in useEffect returns for timers

**Recommendation:**
- Consider adding `nodesDraggable={false}` and `nodesConnectable={false}` on initial mount until user interaction starts
- Add virtualization for large node graphs (ReactFlow supports this via `nodeExtent` limiting)
- For the JdmEditorField, the @gorules/jdm-editor version 1.52.0 is pinned — check for performance updates in newer versions

---

### 2. No Database Indexes Defined on Custom Content Types — **Medium Severity**

**Files:**
- `/cms/src/api/flow/content-types/flow/schema.json`
- `/cms/src/api/jdm/content-types/jdm/schema.json`
- `/cms/src/api/connection/content-types/connection/schema.json`

**Issue:** The content-type schemas define relations and UID fields but don't explicitly define indexes. Strapi handles basic indexing, but queries filtering by `environment.documentId` or `jdmId.$in` could benefit from compound indexes.

**Evidence (flow/schema.json lines 40-50):**
```json
{
  "environment": {
    "type": "relation",
    "relation": "manyToOne",
    "target": "api::environment.environment"
  },
  "group": {
    "type": "relation",
    "relation": "manyToOne",
    "target": "api::group.group"
  }
}
```

**Recommendation:**
Add indexes via a Strapi migration or directly on the Postgres schema:
```sql
CREATE INDEX idx_flows_environment ON flows(environment_id);
CREATE INDEX idx_jdms_environment ON jdms(environment_id);
CREATE INDEX idx_connections_environment ON connections(environment_id);
CREATE INDEX idx_jdms_jdmid ON jdms(jdm_id);
```

---

### 3. Potential N+1 Pattern in importAll — **Medium Severity**

**File:** `/cms/src/plugins/rule-engine/server/src/controllers/sync.ts`
**Lines:** 299-350

**Issue:** The `importAll` handler loops over each engine flow/jdm/connection and makes individual `getFlow()`, `getJdm()` calls plus individual Strapi `create/update` calls. This is O(n) HTTP calls to the engine plus O(n) database writes.

**Evidence (lines 299-330):**
```typescript
// Import flows
for (const flowSummary of engineFlowsResp.flows) {
  const flowDetail = await client.getFlow(flowSummary.id);  // HTTP call per flow
  // ... then Strapi create/update per flow
}

// Import JDMs
for (const jdmSummary of engineJdmsResp.jdms) {
  const jdmDetail = await client.getJdm(jdmSummary.id);  // HTTP call per JDM
  // ...
}
```

**Recommendation:**
- Batch the HTTP calls using `Promise.all()` with concurrency limits
- Consider batched Strapi writes via `createMany` if Strapi 5 supports it, or use `strapi.db.query()` for bulk inserts

---

### 4. Sequential JDM Writes in persistFlowWriteBack — **Low Severity**

**File:** `/cms/src/plugins/rule-engine/server/src/services/publish-core.ts`
**Lines:** 95-110

**Issue:** When writing back engine versions to multiple JDM documents, the loop awaits each update sequentially.

**Evidence:**
```typescript
for (const jdmDoc of jdmDocs) {
  const v = writeBack.jdmEngineVersions?.[jdmDoc.jdmId];
  if (v != null) {
    await strapi
      .documents('api::jdm.jdm')
      .update({ documentId: jdmDoc.documentId, data: { engineVersion: v } });
  }
}
```

**Recommendation:**
Use `Promise.all()`:
```typescript
await Promise.all(
  jdmDocs
    .filter((doc) => writeBack.jdmEngineVersions?.[doc.jdmId] != null)
    .map((doc) =>
      strapi.documents('api::jdm.jdm').update({
        documentId: doc.documentId,
        data: { engineVersion: writeBack.jdmEngineVersions![doc.jdmId] },
      })
    )
);
```

---

### 5. Connection Pool Size May Be Too Small — **Low Severity**

**File:** `/cms/config/database.ts`
**Lines:** 60-62

**Issue:** Default pool settings are `min: 2, max: 10`. For a CMS with heavy admin usage and potentially concurrent publish operations, this may be restrictive.

**Evidence:**
```typescript
pool: { min: env.int('CMS_DB_POOL_MIN', 2), max: env.int('CMS_DB_POOL_MAX', 10) },
```

**Recommendation:**
Consider increasing to `min: 5, max: 20` for production deployments, or set via environment variables:
```bash
CMS_DB_POOL_MIN=5
CMS_DB_POOL_MAX=20
```

---

### 6. Large JSON Payloads in tree/doc Fields — **Low Severity**

**Files:**
- `/cms/src/api/flow/content-types/flow/schema.json` — `tree` field
- `/cms/src/api/jdm/content-types/jdm/schema.json` — `doc` field

**Issue:** These fields store potentially large JSON structures (engine node trees, decision graphs). When loaded in list views or during sync operations, they could strain memory if many records exist.

**Recommendation:**
- Ensure list queries do NOT populate these heavy fields; use `fields: ['flowId', 'method', 'path']` selectors
- The current sync.ts code already does this (fields are scoped in `getCmsFlows`, etc.) ✓

---

### 7. EnvironmentProvider Fetches on Every Route — **Low Severity**

**File:** `/cms/src/plugins/rule-engine/admin/src/index.tsx`

**Issue:** Each route wraps itself with a fresh `<EnvironmentProvider>`, which calls `fetchEnvironments()` on mount. Navigating between plugin pages triggers re-fetches.

**Evidence (lines 52-105):**
```typescript
routes: [
  {
    path: '/',
    Component: async () => {
      // ...
      return () => (
        <EnvironmentProvider>  // New provider on each route
          <SyncPage />
        </EnvironmentProvider>
      );
    },
  },
  // ... repeated for each route
```

**Recommendation:**
Lift `EnvironmentProvider` to a single wrapper around all routes:
```typescript
routes: [
  {
    path: '/',
    Component: async () => {
      return () => <SyncPage />;
    },
  },
  // ...
],
// Wrap at plugin level via bootstrap()
```

---

### 8. No Memory Leak Issues Found — **Good**

**Reviewed files:**
- All hooks in `/cms/src/plugins/rule-engine/admin/src/hooks/`
- All contexts in `/cms/src/plugins/rule-engine/admin/src/contexts/`
- All components

**Findings:**
- Timer cleanup is present in FlowCanvasField and JdmEditorField
- useEffect dependencies are correct
- No missing cleanup patterns
- localStorage usage is appropriate (simple key-value, not growing)
- State is scoped correctly, no global mutable state

---

### 9. Lifecycle Hooks Are Lightweight — **Good**

**File:** `/cms/src/api/connection/content-types/connection/lifecycles.ts`

**Finding:** The only lifecycle hook found does simple in-memory validation (secret denylist check). No database queries or async operations in lifecycles.

---

## Performance Improvement Recommendations (Prioritized)

### High Impact

1. **Add database indexes** on `environment` relation columns and frequently-queried UID fields

2. **Batch HTTP calls in sync importAll** using `Promise.all()` with concurrency limiting (e.g., p-limit)

3. **Increase connection pool size** via environment variables for production

### Medium Impact

4. **Parallelize JDM write-backs** in `persistFlowWriteBack()`

5. **Lift EnvironmentProvider** to reduce redundant API calls

6. **Consider upgrading @gorules/jdm-editor** if newer versions have performance improvements

### Low Impact

7. **Add virtualization to ReactFlow** for flows with many nodes

8. **Profile Strapi baseline** — Strapi 5 itself has significant memory overhead; ensure the server has adequate resources (recommended: 1GB+ RAM for admin panel)

---

## Monitoring Recommendations

1. Enable Postgres query logging to identify slow queries:
   ```sql
   ALTER DATABASE strapi_cms SET log_min_duration_statement = 1000;
   ```

2. Use Node.js `--inspect` flag to profile memory with Chrome DevTools

3. Monitor connection pool usage via pgBouncer or Postgres stats:
   ```sql
   SELECT * FROM pg_stat_activity WHERE datname = 'strapi_cms';
   ```

---

## Conclusion

The codebase is well-structured with no critical issues. Performance problems are likely caused by:
1. Strapi's inherent memory overhead
2. Large JSON payloads in flow trees
3. Unoptimized database queries (missing indexes)
4. Sequential instead of parallel operations in sync/publish

Implementing the high-impact recommendations should measurably improve performance.
