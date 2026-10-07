# Design Review — TASK-008: Scheduled Triggers (Cron) Revision 3

**Reviewer:** Design Review Subagent  
**Worktree:** `/home/nuzirwan/project/rule-engine-api/.worktrees/scheduled-triggers`  
**Verdict:** CHANGES_REQUESTED

---

## Summary

Revision 3 is substantially better than its predecessors. The data model, scheduler loop, distributed-lock protocol, admin API surface, retention logic, and CMS content-type placement are all coherent. Four HIGH gaps survive that would cause compile errors, runtime failures, or silent non-deployment on real infrastructure. Four MEDIUM gaps need resolution before hand-off to the coder.

---

## Findings

### HIGH

**Finding 1 — `withLocation` timezone wrapper is mathematically incorrect**

Section: §2 (Cron Parsing Approach)

The proposed `withLocation` wrapper:
```go
func (l locSchedule) Next(t time.Time) time.Time { return l.inner.Next(t.In(l.loc)) }
```
does not achieve timezone-aware scheduling. `t.In(l.loc)` changes only the display representation; the underlying instant is identical. When `inner.Next` is called, robfig/cron v3's `specSchedule.Next(t)` internally does `t.In(s.Location)` where `s.Location` is UTC (the parser default), overwriting the location set by the wrapper. As a result, `"0 8 * * *"` with `Asia/Jakarta` still fires at 08:00 UTC, not 01:00 UTC. The test case `TestNextRun_timezone` would fail.

**Fix:** Use the `TZ=` prefix approach, which robfig/cron v3 supports natively when the parser is initialised with `cron.Descriptor`:
```go
func ParseSchedule(expr string, loc *time.Location) (cron.Schedule, error) {
    if loc == nil { loc = time.UTC }
    effective := expr
    if loc != time.UTC {
        effective = "TZ=" + loc.String() + " " + expr
    }
    sched, err := cronParser.Parse(effective)
    ...
}
```
The `withLocation` wrapper and the `locSchedule` type can be deleted entirely.

---

**Finding 2 — `newErr` is unexported; scheduler package cannot call it**

Section: §2 `cron.go`, §3 `scheduler.go`

The pseudocode in `scheduler/cron.go` and `scheduler/scheduler.go` calls:
```go
return nil, newErr(config.Validation, "invalid cron expression: "+err.Error())
```
`newErr` is a **package-internal** function in `engine/internal/config/errors.go` (lowercase `n`). It cannot be called from `engine/internal/scheduler/`. The constant `config.Validation` is exported (uppercase) so that is fine, but the function call is a compile error.

**Fix:** The scheduler package must produce validation errors by wrapping the exported sentinels:
```go
return nil, fmt.Errorf("invalid cron expression %q: %w", expr, config.ErrValidation)
```
`errors.Is(err, config.ErrValidation)` still matches, so `statusForAdmin` maps it to 400 correctly. Update every `newErr(config.Validation, ...)` and `newErr(config.NotFound, ...)` reference in `scheduler/` accordingly.

---

**Finding 3 — `valkeyClient` is not accessible in `run()`; Appendix wiring is unimplementable as written**

Section: Appendix (`cmd/engine/main.go` wiring)

The Appendix says:
```go
if valkeyAddr := os.Getenv("VALKEY_ADDR"); valkeyAddr != "" {
    // Reuse the same valkey client opened for the config cache.
    locker = scheduler.NewValkeyLocker(valkeyClient, obsLog)
}
```
But `valkeyClient` does not exist in the scope of `run()`. The `valkey.Client` is a local variable inside `buildStore()`, which returns `(config.Store, httpapi.AdminStore, func(), error)` — the client is never surfaced. The coder has no way to write this as-is.

**Fix:** Choose one of the following (state the choice explicitly in the design):
- **Option A (preferred):** Refactor `buildStore` to return the optional `valkey.Client` as a fifth return value: `(config.Store, httpapi.AdminStore, valkey.Client, func(), error)`. The caller assigns it to `valkeyClient` and passes it to the scheduler.
- **Option B:** The scheduler opens its own separate `valkey.Client` using the same `VALKEY_ADDR` env var. Add a `NewValkeyClient(addr string) (valkey.Client, error)` helper or inline the `valkey.NewClient(...)` call in `run()` after `buildStore` returns.

---

**Finding 4 — Migration 0004 will never be applied to existing deployments**

Section: §1 (Data Model and Migration)

`SchemaHasConfigTables` checks for the existence of the `flows` table as its sole sentinel:
```go
// connect_pg.go line 84
WHERE table_schema=$1 AND table_name='flows'
```
On any deployment that already has migrations 0001–0003 applied, `hasTables = true` and `migrations.Apply` is **skipped entirely**. Migration `0004_schedules.up.sql` (the `schedules` and `schedule_runs` tables) will never be created through the normal engine startup path. This is a silent production failure — the engine would start, the scheduler would run, and every `ListEnabledSchedules` call would return a "relation does not exist" error.

**Fix:** Choose one:
- **Option A:** Document explicitly in §1 and the Appendix that `0004_schedules.up.sql` must be applied for existing deployments via `migrate up` (or an ops runbook step) before rolling out the new binary. The design already notes "`migrate` CLI" is the production path.
- **Option B:** Change the migration SQL to use `CREATE TABLE IF NOT EXISTS schedules` and `CREATE TABLE IF NOT EXISTS schedule_runs` (and drop `IF EXISTS` on the down migration), then update `SchemaHasConfigTables` (or rename to `SchemaHasAllTables`) to check for ALL sentinel tables including `schedules`. This makes `Apply` safe to re-run.

---

### MEDIUM

**Finding 5 — `generateRunID()` is not defined or specified anywhere**

Section: §3 `fireSchedule` pseudocode

`fireSchedule` calls `generateRunID()` to build the run's request/trace ID:
```go
flowCtx = flow.NewCtx(generateRunID(), "", env, triggerInput)
```
No such function exists in the engine codebase. There is no `uuid` import in the existing engine packages (it is an indirect dependency via pgx). The data-plane handler uses the `X-Request-Id` HTTP header; the scheduler has no equivalent.

**Fix:** Define `generateRunID` in `scheduler.go` (or `scheduler/util.go`) and add `github.com/google/uuid` to the direct dependencies in the §8 go.mod change list:
```go
import "github.com/google/uuid"

func generateRunID() string { return "sched-" + uuid.New().String() }
```
The `github.com/google/uuid v1.6.0` package is already an indirect dep; promoting it to direct requires only a `go.mod` direct-dependency entry.

---

**Finding 6 — Manual trigger `POST /admin/schedules/{id}/run` leaves `next_run` stale; double-execution risk is unspecified**

Section: §5 (`POST /admin/schedules/{id}/run` description)

The manual trigger endpoint is defined to: resolve the flow, run it, and record the run via `RecordScheduleRun`. It does NOT call `UpdateScheduleRunTimes`. The design says "no distributed lock" for manual runs, which is fine. However, if a schedule is overdue when the operator triggers it manually (i.e., `next_run` is in the past), the scheduler loop will see `s.NextRun.Before(now)` on its next poll and fire the schedule again — resulting in double execution within the same poll window.

**Fix:** Add an explicit rule to the endpoint description:
> "The manual trigger does NOT advance `next_run`. If the schedule is currently overdue (`next_run` is in the past), the scheduler loop may fire it again on its next poll cycle, creating double execution. This is an accepted v1 limitation for operator-initiated debug triggers. Operators should disable the schedule before using manual triggers on overdue schedules."

Also update `ScheduleAdminStore` to either include `UpdateScheduleRunTimes` (if the endpoint should advance `next_run`) or explicitly exclude it (if not).

---

**Finding 7 — Adapter duplication scope is materially misstated**

Section: §3 (`scheduler/adapters.go` comment)

The design states:
> "These are pure 10-line wrappers; duplication is explicitly preferred over a refactor…"

`httpapi/adapters.go` is **330 lines** implementing `templatingRegistry` (with `Client`, `Reload`, `HealthCheck`, `Close`, `SecretProvider`, `Execute`, `resolvePayload`, `resolveParams`, `resolveTemplates`, `resolveTemplateString`, `lookupPath`, `normalizeResult`) plus `branchingEvaluator`. Copying this wholesale into `scheduler/adapters.go` is a significant maintenance burden, not a 10-line duplication.

The circular-dependency reasoning is correct (scheduler cannot import httpapi), but the description of scope is wrong and will surprise the implementer.

**Fix:** Replace the "pure 10-line wrappers" language with: "The full `templatingRegistry` and `branchingEvaluator` types (∼330 lines total from `httpapi/adapters.go`) must be copied to `scheduler/adapters.go`. A follow-up refactor may extract these to `internal/flowadapter/` (no circular dep since `adapters.go` only imports `connect`, `flow`, `decision` — no `httpapi`-specific types)."

---

**Finding 8 — `uuid` package not listed in go.mod change list**

Section: §8 (Engine changes — go.mod)

The §8 go.mod change entry lists only `github.com/robfig/cron/v3 v3.0.1`. If `generateRunID()` uses `github.com/google/uuid` (as Finding 5 requires), that package must also appear in the §8 change list as a promoted direct dependency.

**Fix:** Add to the §8 go.mod row:
```
github.com/google/uuid v1.6.0   // promote from indirect; used by scheduler.generateRunID
```

---

### NIT

**Finding 9 — `truncateResponse` and `safeErrStr` are referenced but not defined or specified**

Section: §3 `fireSchedule` pseudocode

`fireSchedule` calls `truncateResponse(flowCtx.Response, 4096)` and `safeErrStr(err)`. Neither function is defined in the design or existing codebase. The intent is clear from context, but the implementer needs to know their signatures and placement (presumably `scheduler/util.go`).

**Fix (optional — add a note):** Define these helpers in `scheduler/util.go`:
```go
func safeErrStr(err error) string {
    if err == nil { return "" }
    return err.Error()
}

// truncateResponse JSON-encodes v and caps it at maxBytes, appending
// {"_truncated": true} if truncation occurred. Returns nil on encode error.
func truncateResponse(v map[string]any, maxBytes int) any { ... }
```

---

**Finding 10 — CronBuilder `@every N m/h` input is under-specified for compound durations**

Section: §7 (CMS Plugin — Cron Builder)

The Preset mode describes "a free-form `@every N m/h` input" suggesting separate N and unit fields. robfig/cron accepts any Go duration string after `@every` (e.g., `@every 1h30m`, `@every 90s`). A two-field (number + unit) picker cannot represent compound durations. This is probably intentional for simplicity, but it should be stated.

**Fix:** Add: "The `@every` preset input is intentionally limited to a single numeric value plus a unit selector (minutes/hours). Compound durations (e.g., `1h30m`) must be entered via Custom mode."

---

**Finding 11 — `/schedules` plugin route inconsistency with existing WebhooksPage**

Section: §7 (CMS Plugin — Plugin admin page)

`WebhooksPage.tsx` exists in the pages directory but has **no registered route** in `index.tsx` (confirmed by reading the file). The design adds a `/schedules` route in `index.tsx` for `SchedulesPage` — which is fine, but the design should note this creates an asymmetry with webhooks and confirm the intent.

**Fix (optional — just note it):** Add: "Note: `WebhooksPage` is not currently registered in `index.tsx`; the `/schedules` route is the first dedicated plugin list page. If the intent is to also register a `/webhooks` route, that is out of scope for TASK-008."

---

## Verified Assumptions

| Assumption | Status |
|-----------|--------|
| `GetActiveFlowByID` exists on `*config.PgStore` at `pgstore_admin.go` line 205 | ✅ Verified — exact location confirmed |
| `*config.PgStore` satisfies `FlowResolver` without adding a new method | ✅ Verified |
| `flows.id` column is `text PRIMARY KEY` (for `REFERENCES flows(id)` in schedules) | ✅ Verified in `0001_init.up.sql` |
| `migrations/0004` slot is available (no file occupying it) | ✅ Verified — only 0001–0003 exist |
| `github.com/go:embed *.sql` in migrations.go embeds all `.sql` files | ✅ Verified |
| `flow.Interpreter.Run` signature matches `FlowExecutor` interface | ✅ Verified — `func (ip *Interpreter) Run(ctx context.Context, tree *Node, ver Version, c *Ctx, dep Deps) error` |
| `flow.NewCtx(reqID, traceID, env string, input map[string]any) *Ctx` — 4-arg signature | ✅ Verified |
| `config.ErrValidation`, `config.ErrNotFound`, etc. are exported sentinels | ✅ Verified |
| `valkey.IsValkeyNil(err)` is the correct NX-nil detection pattern | ✅ Verified via `cache.go` usage |
| `valkey-go` is already a dependency at `v1.0.78` | ✅ Verified in `go.mod` |
| `PUBLISHABLE` set in `publish-middleware.ts` currently contains `flow`, `jdm`, `connection` only | ✅ Verified |
| No `api::webhook.webhook` entry in `PUBLISHABLE` (webhooks not publish-middleware synced) | ✅ Verified |
| WebhooksPage exists in pages/ and follows `useFetchClient()` pattern | ✅ Verified |
| CMS `src/api/` content types follow `api::{name}.{name}` UID convention | ✅ Verified |
| `admin.go`'s `requireRole` function already handles `/admin/webhooks` prefix first | ✅ Verified |
| `admin_types.go` does NOT contain the compile assertion (it lives in `webhook_admin.go`) | ✅ Verified — pattern confirmed for `schedule_admin.go` placement |
| `Deps` struct in `server.go` does NOT currently have `ExecTimeout` | ✅ Verified — field to be added |
| `google/uuid v1.6.0` is an indirect dep (can be promoted) | ✅ Verified in `go.mod` |

---

## Unverified / Wrong Assumptions

| Assumption | Status |
|-----------|--------|
| `withLocation` wrapper correctly applies timezone to parsed cron schedule | ❌ **WRONG** — mathematical analysis shows `t.In(l.loc)` has no effect; inner `Next` always re-converts to UTC. See Finding 1. |
| `valkeyClient` is available in `run()` for the scheduler wiring | ❌ **WRONG** — the client is local to `buildStore()` and not returned. See Finding 3. |
| `newErr(config.Validation, ...)` is callable from `scheduler/` package | ❌ **WRONG** — `newErr` is unexported. See Finding 2. |
| Migration 0004 will be applied automatically on engine restart for existing deployments | ❌ **WRONG** — `SchemaHasConfigTables` gates `Apply` on `flows` table existence; existing deployments skip `Apply`. See Finding 4. |
| `adapters.go` duplication is "pure 10-line wrappers" | ❌ **WRONG** — adapters.go is 330 lines. See Finding 7. |
| `generateRunID()` exists or is otherwise available | ❌ **WRONG** — no such function in the codebase. See Finding 5. |

---

## Verdict

**CHANGES_REQUESTED** — 4 HIGH and 4 MEDIUM findings must be resolved before implementation.

The four HIGH findings each produce either a compile error (`newErr`), a silent runtime failure on all non-fresh deployments (migration gating), a mathematical bug (timezone wrapper), or an unimplementable wiring block (`valkeyClient`). None of these are implementation details that can be deferred.

The four MEDIUM findings (`generateRunID`, adapter scope, manual-trigger double-execution, uuid dep) are not blockers by themselves but will cause confusion or incomplete implementation if left unresolved.
