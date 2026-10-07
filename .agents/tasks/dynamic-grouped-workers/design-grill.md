# Design Grill: Dynamic Grouped Flow Workers

## Summary

This design document describes an extension to the rules engine for grouped flow deployment with dynamic worker spawning. The design is comprehensive, covering data models, K8s resources, deployment procedures, and rollback strategies. However, several gaps and ambiguities would block implementation-ready Phase 1 delivery.

**Watch for:** [confirmed] Flow publish transform snippet shows only the transform logic but omits the crucial webhook handler integration that stores flows in the database. [confirmed] The `groups.version` column increment happens on group updates but connection-only changes via the junction table need explicit version increment (the doc shows this in `replaceGroupConnections` but doesn't show it consistently in all update paths). [confirmed] The canary deployment strategy references a `track` label and separate `-canary` deployment/service but the `generate-manifests` CLI doesn't show how to generate canary manifests.

**Verdict**: CHANGES_REQUESTED

---

## High-level View

**Data Model**: The `groups`, `group_connections`, and audit tables are well-specified with DDL. The junction table design with denormalized audit snapshots (`GroupAuditData`) enables accurate rollback including connection changes. The `version` column for hot-reload detection is present and its increment logic is shown in the `Update` method.

**Rollback Strategy**: The design covers four rollback scenarios (group config, flow assignment, worker deployment, gateway config) with RTOs ranging from <1 minute to 5-15 minutes. The `group_audit` table captures full state including connections via the `GroupAuditData` struct. The `Rollback` function properly restores both the group row and the junction table. However, the "full group deletion recovery" procedure depends on `restore-from-audit` which requires K8s manifest regeneration + GitOps sync—the stated 5-15 min RTO is optimistic given PR review requirements.

**Deployment Rules**: The pre-deployment checklist covers 10 items including config validation, image verification, and concurrent deployment detection. The deployment order diagram shows a 5-phase sequence with clear dependencies. Canary and blue-green procedures are described with traffic routing mechanisms (gateway endpoint override, ConfigMap hot-reload, canary `track` label). The deployment freeze policy covers weekends and quarter-end.

**CMS Integration**: Group content type schema is specified with validation regex for `groupId`, scaling mode enum, and resource limits component. The Flow content type modification (add `group` relation) is shown. The webhook handler stores groups on CMS publish. However, the flow publish transform shows only the transform function—the webhook handler that calls it and stores the flow is not shown, creating ambiguity about how flow-to-group assignments actually persist.

**Phase 1 Scope**: Explicitly scoped as "data model only" with gateway mode deferred to Phase 4. Phase 1 deliverables list all required files, migrations, and CMS changes. The Phase 1 Scope Clarification section explicitly states `dispatch.mode` remains `inline` during Phase 1.

---

<details>
<summary>Issues (12)</summary>

1. **Flow webhook handler missing** — The flow publish transform (`TransformCMSFlow`) is shown but the webhook handler that receives CMS flow publish events, calls the transform, and stores the flow with its `group_id` is not shown. The group webhook handler exists (`HandleCMSGroupPublish`) but no corresponding `HandleCMSFlowPublish` that handles the group field. Add the flow webhook handler or clarify if the existing flow webhook handler already exists and just needs modification.

2. **Connection-only update version increment inconsistency** — The `replaceGroupConnections` function correctly increments `groups.version` when connections change. However, if connections are updated via Admin API or CMS webhook without touching the group row, the code path must ensure `replaceGroupConnections` is called (it is), but the Admin API handler (`HandlePutGroup`) doesn't show the connection update logic at all. Clarify whether `HandlePutGroup` handles `connections` array and calls `replaceGroupConnections`.

3. **Canary manifest generation not specified** — The canary deployment strategy requires a separate `worker-{group}-canary` deployment and service with `track: canary` label. The `generate-manifests` CLI doesn't show how to generate canary manifests. Add `--canary` flag to `generate-manifests` or clarify if canary manifests are created ad-hoc during deployment.

4. **Default group K8s manifest timing** — The "Default Group K8s Bootstrap" section explains the chicken-egg problem well but doesn't specify when/who generates the default group's K8s manifests during initial cluster setup. Is it part of the platform bootstrap runbook? Add explicit instructions for initial deployment.

5. **RTO for full group deletion recovery is optimistic** — The stated 5-15 min RTO for full group deletion recovery assumes GitOps sync is fast. In practice, if the PR review is required (which the design implies by mentioning "GitOps: DevOps commits manifests to infra repo, reviews changes in PR"), the RTO could be 30+ minutes. The "Emergency GitOps bypass policy" section helps but the stated RTO should reflect the normal path (PR required) vs emergency path.

6. **Auto-rollback health check metrics not defined** — The `DeploymentWatcher` checks `worker.Ready` but the specific health check failure criteria (what makes Ready=false) isn't defined. Is it the K8s readiness probe? Prometheus metric? Clarify the health signal source.

7. **KEDA ScaledObject Prometheus query assumes metric exists** — The KEDA trigger uses `gateway_dispatch_total{group="orders"}` metric, but this metric is defined as "Gateway metrics" which implies it's exposed by the gateway, not the worker. During cold start (0 replicas), there's no worker to emit metrics. Clarify that this metric is emitted by the gateway (which is always running) so KEDA can scale from 0.

8. **Scaling.Mode validation gap** — The `ValidateGroup` function checks `mode != 'static' || minReplicas > 0` but doesn't validate that `mode` is one of the three valid values. The SQL CHECK constraint catches this at DB level, but validation should fail earlier at API/transform level. Add mode validation.

9. **Flow reassignment audit capture** — The `flow_group_audit` table schema is shown, but no code captures changes to `flow_versions.group_id`. The `FlowStore` would need similar pre/post state capture logic. Add the audit capture code or reference where it will live.

10. **Circuit breaker state persistence** — The circuit breaker for cross-group calls is in-memory (`sync.Mutex`). If the gateway restarts, all circuit breakers reset to closed. This could cause a thundering herd to a broken group. Consider whether this is acceptable or if circuit breaker state should be shared (Redis, etc.).

11. **Delete group flow count check races** — The `Delete` function checks `COUNT(*) FROM flow_versions WHERE group_id = $1` then deletes. If a concurrent transaction assigns a flow between check and delete, the FK constraint catches it, but the error message would be a generic FK violation, not the friendly "N flows still assigned" message. Consider `SELECT FOR UPDATE` or accept the race.

12. **CMS Group flows relation intentionally omitted but not enforced** — The comment says "flows relation is intentionally OMITTED from the group content type" to prevent confusion. However, nothing prevents a CMS operator from manually adding a `flows` relation in Strapi. Consider adding a webhook validation that ignores/warns if a group's flows field is populated.

</details>

---

<details>
<summary>Details</summary>

### Rollback Strategy Completeness

The rollback strategy is the most complete section of the design. The four scenarios cover:

1. **Group configuration rollback**: API-driven, instant DB restore. The `Rollback` function correctly fetches `old_data` from the target audit record and restores both the `groups` row and the `group_connections` junction table. The legacy audit record handling (fallback to `group_connections_audit` if `GroupAuditData.Connections` is nil) is a nice forward-thinking detail.

2. **Flow assignment rollback**: API-driven via `flow_group_audit`. Schema is defined but the capture code isn't shown.

3. **Worker deployment rollback**: K8s-native `kubectl rollout undo`. The design correctly defers to K8s for this rather than reinventing it. The `RecordAutoRollback` function creates an audit trail for automated rollbacks triggered by health check failures.

4. **Gateway configuration rollback**: Git revert + redeploy. Appropriate since gateway config is immutable at runtime.

**Gap in recovery procedure**: The "Full Group Deletion Recovery Procedure" shows 5 steps but step 3 assumes GitOps is available. The "Emergency GitOps bypass" section addresses this but presents it as an exception. For a P1 incident where a production group was accidentally deleted, the emergency path (direct `kubectl apply`) would be the norm, not the exception. The procedure should present emergency path first, normal path second.

The `RestoreFromAudit` function correctly finds the most recent DELETE audit and re-inserts both the group row and connections. However, it doesn't handle the case where the group was deleted multiple times (unlikely but possible if someone restores then deletes again). The `LIMIT 1` in the query returns the most recent delete, which is correct behavior.

### Deployment Rules Completeness

The deployment rules section is thorough. The pre-deployment checklist covers:

- Config validation (CLI command exists)
- Flow/connection reference validation (CLI command exists)
- Image verification
- K8s prerequisites (namespace, secrets, quota)
- Concurrent deployment detection
- Rollback plan documentation

The deployment order diagram correctly shows the dependency chain: Infrastructure → Database → Group Config → Manifest Generation → Worker Deployment → Gateway Activation. The explicit note that "Phase 3a and 3b are sequential dependencies, not alternatives" prevents misunderstanding.

**Canary deployment gap**: The canary strategy shows detailed steps including traffic weight progression (10% → 25% → 50%) and the `CanaryState` struct in `WorkerRegistry`. The CLI commands (`engine groups canary-start`, `canary-weight`, `canary-promote`, `canary-abort`) are specified. However, the actual K8s manifests for the canary deployment (`worker-orders-canary.yaml`) are shown as hand-written YAML, not generated by the CLI. The `generate-manifests` CLI section doesn't mention canary support. This creates ambiguity: is the canary deployment created manually, or does the CLI support a `--canary` flag?

**Blue-green deployment**: Well-specified with the ConfigMap-based endpoint override mechanism. The hot-reload watcher for the ConfigMap is shown. The cleanup procedure correctly notes "You cannot rename a K8s deployment—update the image instead" which is accurate K8s behavior.

**Argo Rollouts alternative**: The design offers Argo Rollouts as an alternative for automated canary analysis. The `AnalysisTemplate` is fully specified with success rate, error rate, and latency metrics. This is a nice "escape hatch" for teams that want more sophisticated canary behavior.

### Phase 1 Completeness Checklist

Phase 1 deliverables as listed:

| Deliverable | Specified | Notes |
|-------------|-----------|-------|
| `config/group.go` — Group, ScalingConfig, ValidateGroup | ✅ | Fully specified with validation logic |
| `config/pgstore_group.go` — PostgreSQL store with audit | ✅ | Update, Delete, Rollback, RestoreFromAudit shown |
| `httpapi/admin_groups.go` — Admin API endpoints | ✅ | HandlePutGroup, HandleDeleteGroup shown |
| `httpapi/webhook_cms.go` — CMS webhook handler | ⚠️ | HandleCMSGroupPublish shown, but HandleCMSFlowPublish not shown |
| `publish/group_transform.go` — CMS → engine transform | ✅ | TransformCMSGroup fully specified |
| `publish/flow_transform.go` — Updated to include group | ⚠️ | TransformCMSFlow shown, but integration unclear |
| Migration: `groups` table | ✅ | DDL with version column |
| Migration: `group_connections` table | ✅ | DDL with cascade delete |
| Migration: `group_audit` table | ✅ | DDL with operation CHECK |
| Migration: `group_connections_audit` table | ✅ | DDL specified |
| Migration: `flow_group_audit` table | ✅ | DDL specified, capture code not shown |
| Migration: `flow_versions.group_id` column | ✅ | ALTER with ON DELETE RESTRICT |
| Strapi: `group` content type | ✅ | Full schema.json |
| Strapi: `config.resource-limits` component | ✅ | Full component spec |
| Strapi: `flow` modification | ✅ | Group relation addition shown |

**Missing from Phase 1 scope**:

1. Flow webhook handler for group assignment storage
2. Flow audit capture code for `flow_group_audit`
3. Admin API handler for `PATCH /admin/flows/{flow_id}` (assigns flow to group)—mentioned in API Changes but no handler code shown

### Worker Hot-Reload Mechanism

The hot-reload mechanism is well-designed:

1. Workers poll `GET /internal/groups/{group_id}/version` every 30 seconds
2. Gateway returns just `{"version": 5}` (lightweight, no full config)
3. If version increased, worker calls `reloadConfig()` to fetch full config

**Concern**: If connections change via the junction table but the `groups` row isn't touched, does the version increment? The `replaceGroupConnections` function shows `UPDATE groups SET version = version + 1`, so yes. But this function is called from `Rollback` and the rollback path—is it called from the normal update path (`HandlePutGroup`)? The handler doesn't show connection update logic.

### Cross-Group Call Design

The cross-group call design introduces a new `type: "flow"` action that routes through the gateway. This enables composition across group boundaries while maintaining isolation.

**Circuit breaker design**: The in-memory circuit breaker with `StateClosed → StateOpen → StateHalfOpen` transitions is standard. The per-group granularity is appropriate. However, if the gateway has multiple replicas, each replica has independent circuit breaker state. A call that fails on replica A won't trip the breaker on replica B. Consider whether this is acceptable (probably yes, since the breaker is for local protection, not global coordination).

### Default Group Connection Access

The design correctly specifies that the default group has NO connections by default:

> Flows in the default group have access to NO connections by default.

This is a secure-by-default posture. Flows that need connections must be explicitly assigned to a group that has those connections, or the default group's connections must be explicitly configured.

</details>

---

## File Map

<details>
<summary>Files referenced</summary>

| Path | Purpose |
|------|---------|
| `.worktrees/dynamic-grouped-workers-phase1/.agents/tasks/dynamic-grouped-workers/design.md` | Primary design document under review |

See full document for inline code snippets covering Go types, SQL DDL, K8s YAML, and Strapi JSON schemas.

</details>
