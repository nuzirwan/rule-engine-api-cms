# TASK-008: Scheduled Triggers (Cron) — Design (Revision 3)

## Overview

This task adds a cron-based scheduler that fires flows on a time-based schedule. The scheduler is a background goroutine wired into the engine at startup (config-store mode only). It polls the `schedules` table, computes the nearest fire time, sleeps until that time, then for each due schedule: acquires a Valkey distributed lock, resolves the flow by ID, invokes the interpreter with a trigger context, records the execution result, releases the lock, and advances `next_run`. All admin CRUD endpoints follow the existing `webhook_admin.go` pattern: thin HTTP layer over a `ScheduleAdminStore` interface backed by `PgStore`. The `POST /admin/schedules/{id}/run` immediate-trigger endpoint shares a `FlowExecutor` interface with the scheduler so both paths are independently testable. The CMS side adds a standard `src/api/` content type (UID `api::schedule.schedule`) and a plugin page with a cron-expression builder — following the existing flow/JDM/connection content-type and publish-middleware pattern.

The four ambiguous points are resolved in §10. Key choices: `github.com/robfig/cron/v3 v3.0.1` as the expression parser (parser-only), 100-run retention window per schedule, lock TTL = 2× execution timeout (default 10 min), max-frequency enforced at validate time.

---

## 1. Data Model and Migration

### Migration file: `engine/migrations/0004_schedules.up.sql`

> The existing migration runner uses `//go:embed *.sql` anchored at `engine/migrations/`. The `004` slot is the next available number after the existing three migrations. If another task occupies `0004` before implementation, renumber to `0005`; the task spec's `005` is an ordinal hint, not a literal constraint.

```sql
-- 0004_schedules.up.sql — cron-based schedule triggers (TASK-008).
--
-- Schedules are MUTABLE (unlike flow/jdm/connection which are immutable-versioned).
-- A schedule is a simple CRUD entity: create, update in-place, delete. There is no
-- publish/rollback versioning because a schedule is operational config, not a content
-- artifact. The only append-only table is schedule_runs (execution history).
--
-- schedule_runs retains at most 100 rows per schedule (pruned in the same transaction
-- that inserts a new row — enforced in application code, not DB triggers).

-- ---- schedule identity + config (mutable) ----
CREATE TABLE schedules (
    id           text PRIMARY KEY,                        -- caller-assigned stable id, e.g. "daily-report"
    name         text NOT NULL,                           -- human display name
    schedule     text NOT NULL,                           -- cron expr or @alias or @every Nd
    timezone     text NOT NULL DEFAULT 'UTC',             -- IANA timezone, e.g. "Asia/Jakarta"
    flow_id      text NOT NULL REFERENCES flows(id),      -- flow to trigger
    input        jsonb NOT NULL DEFAULT '{}',             -- static JSON passed to flow as trigger.input
    enabled      boolean NOT NULL DEFAULT true,           -- false => scheduler skips it entirely
    env          text NOT NULL DEFAULT '',                -- engine env (empty = default)
    last_run     timestamptz,                             -- wall time of last trigger attempt
    next_run     timestamptz,                             -- pre-computed next fire time (UTC)
    created_at   timestamptz NOT NULL DEFAULT now(),
    created_by   text NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   text NOT NULL
);

-- ---- execution history (append-only, bounded to last 100 per schedule) ----
CREATE TABLE schedule_runs (
    id             bigserial PRIMARY KEY,
    schedule_id    text NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
    scheduled_time timestamptz NOT NULL,                  -- when it was supposed to fire
    started_at     timestamptz NOT NULL DEFAULT now(),    -- wall time acquire-lock succeeded
    finished_at    timestamptz,                           -- wall time flow returned (or timed out)
    duration_ms    bigint,                                -- finished_at - started_at in milliseconds
    status         text NOT NULL,                         -- "success"|"failure"|"timeout"|"skipped"
    error          text,                                  -- error message if status != success
    response       jsonb                                  -- truncated flow response summary (first 4KB)
);

-- ---- indexes ----
CREATE INDEX idx_schedules_enabled_next ON schedules (enabled, next_run ASC)
    WHERE enabled = true;                                 -- scheduler hot-path: next due schedule
CREATE INDEX idx_schedules_env ON schedules (env);        -- admin list by env
CREATE INDEX idx_schedule_runs_schedule_at
    ON schedule_runs (schedule_id, started_at DESC);      -- history query
```

```sql
-- 0004_schedules.down.sql
DROP TABLE IF EXISTS schedule_runs;
DROP TABLE IF EXISTS schedules;
```

### Go structs: `engine/internal/config/schedule.go`

```go
// Schedule is a cron-based trigger configuration. Unlike flows/webhooks, schedules
// are mutable (updated in-place, no immutable version history).
type Schedule struct {
    ID        string         `json:"id"`
    Name      string         `json:"name"`
    Schedule  string         `json:"schedule"`          // cron expr or @alias
    Timezone  string         `json:"timezone"`          // IANA, e.g. "Asia/Jakarta"
    FlowID    string         `json:"flowId"`
    Input     map[string]any `json:"input"`             // static flow input
    Enabled   bool           `json:"enabled"`
    Env       string         `json:"env"`
    LastRun   *time.Time     `json:"lastRun,omitempty"`
    NextRun   *time.Time     `json:"nextRun,omitempty"`
    CreatedAt time.Time      `json:"createdAt"`
    UpdatedAt time.Time      `json:"updatedAt"`
}

// ScheduleRun records one execution attempt for audit and debug.
type ScheduleRun struct {
    ID            int64      `json:"id"`
    ScheduleID    string     `json:"scheduleId"`
    ScheduledTime time.Time  `json:"scheduledTime"`  // when it was supposed to fire
    StartedAt     time.Time  `json:"startedAt"`
    FinishedAt    *time.Time `json:"finishedAt,omitempty"`
    DurationMs    *int64     `json:"durationMs,omitempty"`
    Status        string     `json:"status"`          // "success"|"failure"|"timeout"|"skipped"
    Error         string     `json:"error,omitempty"`
    Response      any        `json:"response,omitempty"` // truncated flow response
}

// Schedule run status constants.
const (
    RunStatusSuccess = "success"
    RunStatusFailure = "failure"
    RunStatusTimeout = "timeout"
    // RunStatusSkipped: lock was not acquired — either another instance ran this
    // schedule (normal distributed dedup) OR the lock backend (Valkey) was
    // unavailable at acquire time (the error field distinguishes them). Both cases
    // mean this instance did not execute the flow. A v2 enhancement could introduce
    // RunStatusLockError = "lock_error" to separate these, but v1 accepts the
    // dual-use and relies on the error field for disambiguation.
    RunStatusSkipped = "skipped"
)
```

The store interface lives in `schedule.go` and is implemented in `pg_store_schedule.go`:

```go
// ScheduleStore is the narrow interface the scheduler and admin handlers depend on.
// Satisfied structurally by *config.PgStore.
type ScheduleStore interface {
    ListEnabledSchedules(ctx context.Context, env string) ([]Schedule, error)
    GetSchedule(ctx context.Context, env, id string) (Schedule, error)
    CreateSchedule(ctx context.Context, env string, s Schedule) error
    UpdateSchedule(ctx context.Context, env string, s Schedule) error
    DeleteSchedule(ctx context.Context, env, id string) error
    ListSchedules(ctx context.Context, env string) ([]Schedule, error)
    UpdateScheduleRunTimes(ctx context.Context, env, id string, lastRun, nextRun time.Time) error
    RecordScheduleRun(ctx context.Context, env string, run ScheduleRun) (int64, error)
    ListScheduleRuns(ctx context.Context, env, scheduleID string, limit int) ([]ScheduleRun, error)
}
```

`UpdateScheduleRunTimes` is the hot-path method called after each execution: updates `last_run` and `next_run` in one statement. `RecordScheduleRun` inserts a run row and prunes rows beyond 100 in the same transaction.

---

## 2. Cron Parsing Approach

**Chosen library: `github.com/robfig/cron/v3 v3.0.1`** (pinned exact version, BSD-2-Clause license).

Justification:
- Handles all required features: standard 5-field `min hour day month weekday`, ranges (`1-5`), steps (`*/5`), lists (`1,3,5`), @-aliases (`@hourly`, `@daily`, `@weekly`, `@monthly`), and `@every` (`@every 5m`, `@every 1h`).
- Parser-only usage: only the `Schedule` interface's `.Next(time.Time) time.Time` method is called. The library's own goroutine-based cron runner is **never started**.
- Zero transitive dependencies. ~1.7k GitHub stars, actively maintained.
- Native timezone support via `cron.NewParser` with the `cron.Descriptor` flag.

**Single parser construction** (finding 6 fix):

```go
var cronParser = cron.NewParser(
    cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)
```

`cron.NewParser(...Descriptor).Parse(expr)` handles all expression types: 5-field standard cron, `@hourly`/`@daily`/`@weekly`/`@monthly`, and `@every 5m`. There is no need for a separate `ParseStandard` call.

**File: `engine/internal/scheduler/cron.go`**

```go
// ParseSchedule parses a cron expression or @-alias into a robfig/cron Schedule.
// It uses a single parser instance that covers 5-field standard cron, @-aliases,
// and @every directives. The max-frequency gate (≥ 1 minute) is enforced here via
// ValidateInterval. Returns a config.Validation error on any failure.
func ParseSchedule(expr string, loc *time.Location) (cron.Schedule, error) {
    if loc == nil {
        loc = time.UTC
    }
    // robfig/cron parses in UTC by default; timezone is applied via withLocation
    // wrapper below.
    sched, err := cronParser.Parse(expr)
    if err != nil {
        return nil, newErr(config.Validation, "invalid cron expression: "+err.Error())
    }
    if err := ValidateInterval(sched); err != nil {
        return nil, err
    }
    return withLocation(sched, loc), nil
}

// NextRun computes the next fire time for the parsed schedule starting from 'from'.
func NextRun(sched cron.Schedule, from time.Time) time.Time {
    return sched.Next(from)
}

// ValidateInterval rejects any expression whose minimum natural interval is
// < 1 minute (max-frequency enforcement).
func ValidateInterval(sched cron.Schedule) error {
    t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
    t1 := sched.Next(t0)
    if t1.Sub(t0) < time.Minute {
        return newErr(config.Validation, "schedule interval must be at least 1 minute")
    }
    return nil
}

// withLocation wraps a Schedule to shift Next() results into loc.
// robfig/cron does not expose a timezone option on the parser directly;
// the standard approach is to wrap the schedule.
type locSchedule struct {
    inner cron.Schedule
    loc   *time.Location
}
func (l locSchedule) Next(t time.Time) time.Time { return l.inner.Next(t.In(l.loc)) }
func withLocation(s cron.Schedule, loc *time.Location) cron.Schedule {
    if loc == time.UTC { return s }
    return locSchedule{inner: s, loc: loc}
}
```

**Max-frequency enforcement** is a single check in `ValidateInterval`:
```
t1 := sched.Next(t0); if t1.Sub(t0) < time.Minute { return validation error }
```

For standard 5-field cron the minimum natural period is already 1 minute (the `minute` field is the finest granularity), so this check always passes for valid standard expressions. For `@every` expressions the check catches sub-minute durations (e.g., `@every 30s`).

**go.mod addition:**
```
github.com/robfig/cron/v3 v3.0.1
```

---

## 3. Scheduler Service Loop

### File: `engine/internal/scheduler/scheduler.go`

**FlowExecutor interface** (finding 1 fix):

```go
// FlowExecutor is the interpreter seam the scheduler uses to run a flow.
// Satisfied by *flow.Interpreter. Defined here so the scheduler and scheduleAdmin
// are independently testable with a fake.
type FlowExecutor interface {
    Run(ctx context.Context, node *flow.Node, ver flow.Version, c *flow.Ctx, deps flow.Deps) error
}
```

**FlowResolver interface:**

```go
// FlowResolver looks up an active flow version by flow ID (not by HTTP route).
// *config.PgStore already satisfies this interface via GetActiveFlowByID in
// pgstore_admin.go (line 205). No new method implementation is needed in
// pg_store_schedule.go — adding one would be a compile error ("method redeclared").
type FlowResolver interface {
    GetActiveFlowByID(ctx context.Context, env, flowID string) (config.FlowVersion, error)
}
```

**Config struct** (finding 9 fix):

```go
// Config holds the constructor options for a Scheduler.
type Config struct {
    Store       config.ScheduleStore
    FlowStore   FlowResolver
    Executor    FlowExecutor          // *flow.Interpreter satisfies this
    FlowDeps    flow.Deps             // conns, decide, trace, log (not request-scoped)
    Locker      DistributedLocker     // nil = single-instance mode (no Valkey)
    Log         observ.Logger
    Env         string                // engine logical env (always "" in v1)
    ExecTimeout time.Duration         // default 5m; overridden by SCHEDULER_EXEC_TIMEOUT
    PollInterval time.Duration        // default 30s; how often to reload schedule list from DB
}
```

**Scheduler struct:**

```go
type Scheduler struct {
    store        config.ScheduleStore
    flowStore    FlowResolver
    executor     FlowExecutor
    deps         flow.Deps
    locker       DistributedLocker
    log          observ.Logger
    env          string
    execTimeout  time.Duration
    pollInterval time.Duration
    stopCh       chan struct{}
    doneCh       chan struct{}
}

func New(cfg Config) *Scheduler {
    if cfg.ExecTimeout <= 0 { cfg.ExecTimeout = 5 * time.Minute }
    if cfg.PollInterval <= 0 { cfg.PollInterval = 30 * time.Second }
    return &Scheduler{
        store:        cfg.Store,
        flowStore:    cfg.FlowStore,
        executor:     cfg.Executor,
        deps:         cfg.FlowDeps,
        locker:       cfg.Locker,
        log:          cfg.Log,
        env:          cfg.Env,
        execTimeout:  cfg.ExecTimeout,
        pollInterval: cfg.PollInterval,
        stopCh:       make(chan struct{}),
        doneCh:       make(chan struct{}),
    }
}
```

**Main loop algorithm:**

```
Start(ctx):
  go func():
    defer close(doneCh)
    backoff := 1s
    for:
      schedules, err = store.ListEnabledSchedules(ctx, env)
      if err:
        log.warn("scheduler: load schedules failed", err)
        select:
          case <-time.After(min(backoff, 30s)): backoff = min(backoff*2, 30s)
          case <-stopCh: return
        continue
      backoff = 1s

      now = time.Now().UTC()
      for each s in schedules:
        if s.NextRun != nil && s.NextRun.Before(now.Add(100ms)):
          go fireSchedule(ctx, s, *s.NextRun)

      // Sleep until next due schedule or pollInterval, whichever is sooner.
      nextDue = earliest NextRun in non-fired schedules; default = now + pollInterval
      sleep = min(nextDue.Sub(now), pollInterval)
      if sleep < 0: sleep = 0

      select:
        case <-time.After(sleep):
        case <-stopCh: return

Stop():
  close(stopCh)
  <-doneCh
```

**fireSchedule:**

```
fireSchedule(ctx, schedule, scheduledTime):
  execCtx, cancel = context.WithTimeout(ctx, execTimeout)
  defer cancel()

  // Distributed lock: skip if another instance owns it.
  lockKey = "schedule:" + schedule.ID + ":lock"
  lockToken = uuid.New().String()
  lockTTL = 2 * execTimeout

  if locker != nil:
    acquired, err = locker.TryAcquire(execCtx, lockKey, lockToken, lockTTL)
    if err != nil:
      log.warn("scheduler: lock acquire error", schedule.ID, err)
      // Fail open: record skipped and return.
      recordRun(status=skipped, error=err.Error())
      return
    if !acquired:
      recordRun(status=skipped)
      return
    defer locker.Release(ctx, lockKey, lockToken)
    heartbeatCtx, stopHeartbeat = context.WithCancel(execCtx)
    defer stopHeartbeat()
    go locker.Heartbeat(heartbeatCtx, lockKey, lockToken, lockTTL)

  startedAt = time.Now().UTC()

  // Resolve the active flow by ID.
  fv, err = flowStore.GetActiveFlowByID(execCtx, env, schedule.FlowID)
  if err != nil:
    finishRun(RunStatusFailure, err)
    return

  // Build trigger input following the spec's required shape.
  triggerInput = map[string]any{
    "trigger": map[string]any{
      "type":          "schedule",
      "scheduleId":    schedule.ID,
      "scheduledTime": scheduledTime.Format(time.RFC3339),
      "actualTime":    startedAt.Format(time.RFC3339),
    },
    "input": schedule.Input,
  }
  flowCtx = flow.NewCtx(generateRunID(), "", env, triggerInput)

  runDeps = flow.Deps{
    Conns:  newTemplatingRegistry(deps.Conns, flowCtx),
    Decide: newBranchingEvaluator(deps.Decide),
    Trace:  deps.Trace,
    Log:    deps.Log,
  }

  err = executor.Run(execCtx, &fv.Tree, flow.Version{FlowID: fv.FlowID, Version: fv.Version}, flowCtx, runDeps)

  finishedAt = time.Now().UTC()
  status = RunStatusSuccess
  if err != nil:
    status = RunStatusFailure
    if errors.Is(err, context.DeadlineExceeded): status = RunStatusTimeout

  // Compute next run time in the schedule's timezone.
  loc, _ = time.LoadLocation(schedule.Timezone)  // validated at creation; safe
  parsed, _ = ParseSchedule(schedule.Schedule, loc)
  nextRun = NextRun(parsed, scheduledTime)

  // Persist results (best-effort: log failures, don't panic).
  store.UpdateScheduleRunTimes(ctx, env, schedule.ID, startedAt, nextRun)
  store.RecordScheduleRun(ctx, env, ScheduleRun{
    ScheduleID:    schedule.ID,
    ScheduledTime: scheduledTime,
    StartedAt:     startedAt,
    FinishedAt:    &finishedAt,
    DurationMs:    ptr(finishedAt.Sub(startedAt).Milliseconds()),
    Status:        status,
    Error:         safeErrStr(err),
    Response:      truncateResponse(flowCtx.Response, 4096),
  })
```

**Templating/evaluator adapters** (finding 5 fix): `newTemplatingRegistry` and `newBranchingEvaluator` are defined in `httpapi/adapters.go`. The scheduler needs the same two adapters. Duplicating them into `internal/scheduler/adapters.go` avoids importing `httpapi` from `scheduler` (circular dep) while keeping the refactor scope minimal. These are pure 10-line wrappers; duplication is explicitly preferred over a refactor that touches the hot path. Noted as a follow-up cleanup item.

---

## 4. Distributed Lock Protocol

### File: `engine/internal/scheduler/lock.go`

```go
// DistributedLocker is the lock interface the scheduler depends on.
// A nil implementation means single-instance mode (no Valkey).
type DistributedLocker interface {
    // TryAcquire attempts SET key token NX PX ttlMs.
    // Returns (true, nil) on success, (false, nil) when key already exists,
    // (false, err) on transport error.
    TryAcquire(ctx context.Context, key, token string, ttl time.Duration) (bool, error)

    // Release deletes the key only if its value matches token (Lua CAS delete).
    // Errors are logged at warn level and swallowed — TTL is the safety backstop.
    Release(ctx context.Context, key, token string) error

    // Heartbeat renews the lock every ttl/3 until ctx is cancelled or lock loss
    // is detected. On lock loss (Lua conditional renew returns 0), it logs at
    // warn level and returns — the caller's execCtx expires on its own deadline.
    Heartbeat(ctx context.Context, key, token string, ttl time.Duration)
}
```

**ValkeyLocker implementation:**

```go
type ValkeyLocker struct {
    client valkey.Client
    log    observ.Logger
}

func NewValkeyLocker(client valkey.Client, log observ.Logger) *ValkeyLocker {
    return &ValkeyLocker{client: client, log: log}
}
```

**TryAcquire:**
```go
cmd = client.B().Set().Key(key).Value(token).Nx().PxMilliseconds(ttl.Milliseconds()).Build()
err = client.Do(ctx, cmd).Error()
if valkey.IsValkeyNil(err): return false, nil  // key already exists
if err != nil: return false, wrapErr(Upstream, "lock acquire", err)
return true, nil
```

**Release (Lua CAS delete):**
```go
const releaseLua = `
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end`

err = client.Do(ctx, client.B().Eval().Script(releaseLua).Numkeys(1).Key(key).Arg(token).Build()).Error()
if err != nil {
    l.log.Emit(ctx, "warn", "scheduler: lock release error", map[string]any{"key": key, "err": err})
}
return nil  // always nil — TTL backstop covers release failures
```

**Heartbeat goroutine** (finding 9 comment fix):
```go
func (l *ValkeyLocker) Heartbeat(ctx context.Context, key, token string, ttl time.Duration) {
    ticker := time.NewTicker(ttl / 3)
    defer ticker.Stop()
    const renewLua = `
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("pexpire", KEYS[1], ARGV[2])
else
    return 0
end`
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            result, err := client.Do(ctx, client.B().Eval().
                Script(renewLua).Numkeys(1).Key(key).
                Arg(token, strconv.FormatInt(ttl.Milliseconds(), 10)).
                Build()).ToInt64()
            if err != nil || result == 0 {
                // Lock was lost or Valkey error. Log at warn and return.
                // The caller's execCtx expires on its own timeout; there is no
                // mechanism to cancel the in-flight interpreter.Run externally.
                l.log.Emit(ctx, "warn", "scheduler: lock heartbeat lost",
                    map[string]any{"key": key, "err": err, "result": result})
                return
            }
        }
    }
}
```

**Heartbeat failure semantics**: When the heartbeat detects the lock was lost (Lua returns 0), it logs at warn level and returns. The scheduler's `execCtx` has a hard timeout (`execTimeout`); there is no external mechanism to cancel a running `flow.Interpreter.Run`. The lock TTL (2× execTimeout) is the hard concurrency bound: even if a heartbeat is lost, the lock expires before the flow could start a second concurrent execution on another instance.

**Single-instance mode (no Valkey)**: when `VALKEY_ADDR` is unset, `locker` is `nil`. In `fireSchedule`, `nil` locker means the lock acquire step is skipped entirely — the scheduler proceeds and logs a startup warning:
```
"warn: scheduler running without distributed lock (VALKEY_ADDR not set); safe for single-instance deployments only"
```

**Lock key**: `schedule:{id}:lock` where `{id}` is the schedule's stable text ID.

**Lock TTL policy**:
- `execTimeout` defaults to 5 minutes (`SCHEDULER_EXEC_TIMEOUT` env var, e.g., `10m`).
- `lockTTL = 2 × execTimeout` = 10 minutes by default.
- Heartbeat renews at `lockTTL/3` ≈ 3.3 minutes.
- Maximum allowed `execTimeout` is 30 minutes (lockTTL cap of 60 minutes); validated in `execTimeoutFromEnv()`.

---

## 5. Admin API Handler Shapes

### File: `engine/internal/httpapi/schedule_admin.go`

Pattern mirrors `webhook_admin.go` exactly: narrow interface, struct receiver, `mount()`, request/response types, handlers, inline compile assertion.

**ScheduleAdminStore interface:**
```go
type ScheduleAdminStore interface {
    ListSchedules(ctx context.Context, env string) ([]config.Schedule, error)
    GetSchedule(ctx context.Context, env, id string) (config.Schedule, error)
    CreateSchedule(ctx context.Context, env string, s config.Schedule) error
    UpdateSchedule(ctx context.Context, env string, s config.Schedule) error
    DeleteSchedule(ctx context.Context, env, id string) error
    ListScheduleRuns(ctx context.Context, env, id string, limit int) ([]config.ScheduleRun, error)
    RecordScheduleRun(ctx context.Context, env string, run config.ScheduleRun) (int64, error)
}

// Compile-time assertion — lives in schedule_admin.go, not admin_types.go
// (finding 3 fix: matches the webhook_admin.go precedent).
var _ ScheduleAdminStore = (*config.PgStore)(nil)
```

**scheduleAdmin struct** (finding 1 fix — executor + flowStore + deps injected):
```go
type scheduleAdmin struct {
    store       ScheduleAdminStore
    flowStore   scheduler.FlowResolver
    executor    scheduler.FlowExecutor
    deps        flow.Deps
    execTimeout time.Duration
    guard       *auth.OperatorGuard
    env         string
    log         observ.Logger
}

func newScheduleAdmin(
    store ScheduleAdminStore,
    flowStore scheduler.FlowResolver,
    executor scheduler.FlowExecutor,
    deps flow.Deps,
    execTimeout time.Duration,
    guard *auth.OperatorGuard,
    log observ.Logger,
) *scheduleAdmin {
    if execTimeout <= 0 { execTimeout = 5 * time.Minute }
    return &scheduleAdmin{
        store: store, flowStore: flowStore, executor: executor,
        deps: deps, execTimeout: execTimeout,
        guard: guard, env: defaultEnv, log: log,
    }
}
```

In `admin.go`'s `mount()`, pass `a.interp` (which satisfies `FlowExecutor`) and the resolved `adminStore` as `flowStore`. `ExecTimeout` is a new field added to `httpapi.Deps` (Option A from the review); `flow.Deps` is assembled inline from existing `Deps` fields. Both the `ScheduleAdminStore` and `FlowResolver` assertions use the safe two-value form — a missing assertion skips schedule-route mounting with a warn log rather than panicking:

```go
if sa, ok := a.deps.Admin.(ScheduleAdminStore); ok {
    fr, ok2 := a.deps.Admin.(scheduler.FlowResolver)
    if !ok2 {
        // deps.Admin does not satisfy FlowResolver (e.g. a test fake).
        // Log a startup warning and skip mounting schedule routes.
        if a.log != nil {
            a.log.Emit(context.Background(), "warn",
                "schedule admin: deps.Admin does not satisfy scheduler.FlowResolver; schedule routes not mounted", nil)
        }
    } else {
        sched := newScheduleAdmin(
            sa,
            fr,
            a.interp,   // *flow.Interpreter satisfies FlowExecutor
            flow.Deps{Conns: a.deps.Conns, Decide: a.deps.Decide,
                      Trace: a.deps.Trace, Log: a.deps.Log},
            a.deps.ExecTimeout,   // time.Duration added to httpapi.Deps (see §8)
            a.guard,
            a.log,
        )
        sched.mount(mux)
    }
}
```

**Routes:**
```go
func (sa *scheduleAdmin) mount(mux *http.ServeMux) {
    h := func(fn http.HandlerFunc) http.Handler { return sa.guard.Protect(fn) }
    mux.Handle("GET /admin/schedules",           h(sa.listSchedules))
    mux.Handle("POST /admin/schedules",          h(sa.createSchedule))
    mux.Handle("GET /admin/schedules/{id}",      h(sa.getSchedule))
    mux.Handle("PUT /admin/schedules/{id}",      h(sa.updateSchedule))
    mux.Handle("DELETE /admin/schedules/{id}",   h(sa.deleteSchedule))
    mux.Handle("POST /admin/schedules/{id}/run", h(sa.triggerRun))
    mux.Handle("GET /admin/schedules/{id}/runs", h(sa.listRuns))
}
```

**RBAC** (added to `requireRole` in `admin.go`):
```go
if strings.HasPrefix(path, "/admin/schedules") {
    switch r.Method {
    case http.MethodGet:
        return "schedule.read"
    case http.MethodPost, http.MethodPut, http.MethodDelete:
        return "schedule.write"
    }
}
```

**Request/response shapes:**

```go
type createScheduleRequest struct {
    ID       string         `json:"id"`
    Name     string         `json:"name"`
    Schedule string         `json:"schedule"`  // cron expr, required
    Timezone string         `json:"timezone"`  // IANA, defaults to "UTC"
    FlowID   string         `json:"flowId"`    // required
    Input    map[string]any `json:"input"`     // optional, defaults to {}
    Enabled  *bool          `json:"enabled"`   // optional, defaults to true
    Env      string         `json:"env"`       // validated: must be "" or omitted
}

type updateScheduleRequest struct {
    Name     *string         `json:"name,omitempty"`
    Schedule *string         `json:"schedule,omitempty"`
    Timezone *string         `json:"timezone,omitempty"`
    FlowID   *string         `json:"flowId,omitempty"`
    Input    *map[string]any `json:"input,omitempty"`
    Enabled  *bool           `json:"enabled,omitempty"`
}
```

**Validation rules** (enforced in handler before touching the store):

| Field | Rule | Failure |
|-------|------|---------|
| `id` | Required, non-empty, `[a-z0-9\-_]+` — rationale: the schedule ID is embedded verbatim in Valkey lock keys (`schedule:{id}:lock`); restricting to lowercase alphanumerics, hyphens, and underscores prevents key injection and ensures Redis key-safe characters. This is intentionally stricter than the webhook ID (non-empty only) and that choice is documented here. | 400 |
| `name` | Required, non-empty, max 256 chars | 400 |
| `schedule` | Required; parsed via `scheduler.ParseSchedule`; error includes parse detail | 400 |
| `timezone` | Optional (default "UTC"); validated via `time.LoadLocation` | 400 |
| `flowId` | Required, non-empty | 400 |
| `input` | Optional; must be JSON object (not array/scalar) | 400 |
| `enabled` | Optional boolean; defaults to true on create | 400 |

**`POST /admin/schedules/{id}/run`** — immediate trigger:

This endpoint fires the schedule NOW, outside the scheduler loop. It:
1. Fetches the schedule from the store (NotFound → 404).
2. Resolves the flow via `flowStore.GetActiveFlowByID`.
3. Runs the flow via `executor.Run` synchronously in the HTTP handler, with its own `execTimeout`-bounded context.
4. Records the run via `store.RecordScheduleRun` with `scheduled_time = now`.
5. No distributed lock is acquired (manual trigger bypasses multi-instance dedup by design — intended for operator testing).
6. Returns: `{ "runId": 42, "status": "success"|"failure"|"timeout", "durationMs": 1234 }`.

Because this is operator-only and blocks the HTTP connection, `execTimeout` caps the wait. For long-running flows, operators should use the scheduler loop and check run history.

**Error mapping**: reuses existing `statusForAdmin`; invalid cron expression → `config.ErrValidation` → 400. Duplicate schedule ID → constraint violation → `config.ErrValidation` → 400 (no new error class needed).

---

## 6. Execution History Storage and Retention

**Retention: 100 executions per schedule** (fixed in v1; not configurable).

Implemented in the `RecordScheduleRun` Postgres method (in `pg_store_schedule.go`):

```sql
-- Both in one transaction:

-- 1. Insert new run
INSERT INTO schedule_runs (schedule_id, scheduled_time, started_at, finished_at,
                           duration_ms, status, error, response)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- 2. Prune to last 100 (same transaction, atomic with insert)
DELETE FROM schedule_runs
WHERE id IN (
    SELECT id FROM schedule_runs
    WHERE schedule_id = $1
    ORDER BY started_at DESC
    OFFSET 100
);
```

The table never exceeds 101 rows per schedule momentarily (insert then prune in one tx).

**Response truncation**: `flowCtx.Response` is JSON-encoded and capped at 4096 bytes before storage. Larger responses are truncated and a `"_truncated": true` marker is appended to the stored JSON object.

**`GET /admin/schedules/{id}/runs`** response shape:

```json
{
  "runs": [
    {
      "id": 42,
      "scheduleId": "daily-report",
      "scheduledTime": "2026-10-05T08:00:00Z",
      "startedAt":     "2026-10-05T08:00:01.234Z",
      "finishedAt":    "2026-10-05T08:00:02.100Z",
      "durationMs":    866,
      "status":        "success",
      "error":         "",
      "response":      { "reportSent": true }
    }
  ]
}
```

Default limit: 20. Caller may pass `?limit=N` (max capped at 100 in the store).

---

## 7. CMS Content Type and UI

### Content type placement (finding 2 fix)

The existing five content types all live under `cms/src/api/` with `api::` UIDs. The schedule content type follows the same convention:

**File: `cms/src/api/schedule/content-types/schedule/schema.json`** (UID: `api::schedule.schedule`)

```json
{
  "kind": "collectionType",
  "collectionName": "schedules",
  "info": {
    "singularName": "schedule",
    "pluralName": "schedules",
    "displayName": "Schedule",
    "description": "A cron-based schedule that triggers a flow at a specified time interval (TASK-008)."
  },
  "options": {
    "draftAndPublish": true
  },
  "pluginOptions": {},
  "attributes": {
    "scheduleId":  { "type": "uid", "required": true },
    "name":        { "type": "string", "required": true, "maxLength": 256 },
    "cronExpression": { "type": "string", "required": true, "maxLength": 256 },
    "timezone":    { "type": "string", "maxLength": 64 },
    "flowId": {
      "type": "relation",
      "relation": "manyToOne",
      "target": "api::flow.flow"
    },
    "input":       { "type": "json" },
    "enabled":     { "type": "boolean", "default": true },
    "environment": {
      "type": "relation",
      "relation": "manyToOne",
      "target": "api::environment.environment"
    },
    "engineLastRunAt":  { "type": "datetime" },
    "engineNextRunAt":  { "type": "datetime" },
    "lastSyncStatus": {
      "type": "enumeration",
      "enum": ["none", "synced", "failed"],
      "default": "none"
    }
  }
}
```

`engineLastRunAt` and `engineNextRunAt` are read-only fields written back from the engine after sync.

Also add controller/service/route stubs following the webhook pattern:
- `cms/src/api/schedule/controllers/schedule.ts`
- `cms/src/api/schedule/routes/schedule.ts`
- `cms/src/api/schedule/services/schedule.ts`

### Publish middleware (finding 2 fix — no lifecycle hooks)

The sync mechanism is **not** a Strapi lifecycle hook (no `lifecycles.ts` file). It mirrors the `api::connection.connection` pattern in `cms/src/publish-middleware.ts`: add `'api::schedule.schedule'` to the `PUBLISHABLE` set and add a `runSchedulePublish` branch to `buildPublishMiddleware`. On publish the middleware calls `adminClient.createSchedule()` or `adminClient.updateSchedule()` and writes back `lastSyncStatus`. Note: webhooks do **NOT** go through this mechanism — their sync is driven from the WebhooksPage UI, not via `buildPublishMiddleware`, so there is no `runWebhookPublish` precedent to follow.

```typescript
// In publish-middleware.ts:
const PUBLISHABLE = new Set([
  'api::flow.flow',
  'api::jdm.jdm',
  'api::connection.connection',
  'api::schedule.schedule',   // add — follows the connection pattern
]);

// In buildPublishMiddleware handler body:
if (context.uid === 'api::schedule.schedule') {
  // call runSchedulePublish (new function in publish-core.ts)
}
```

### Plugin admin page: `cms/src/plugins/rule-engine/admin/src/pages/SchedulesPage.tsx`

Mirrors `WebhooksPage.tsx` exactly: `useFetchClient()` data fetch from `/api/schedules?populate=flowId,environment`, a Table with columns [ID, Name, Cron Expression, Timezone, Flow, Enabled, Status], Create button → content manager create page, row click → content manager edit page.

```tsx
interface ScheduleEntry {
  id: number;
  documentId: string;
  scheduleId: string;
  name: string;
  cronExpression: string;
  timezone: string;
  flowId?: { flowId: string } | null;
  enabled: boolean;
  lastSyncStatus: 'none' | 'synced' | 'failed';
}
```

Add route to plugin `index.tsx`:
```tsx
{
  path: '/schedules',
  Component: async () => {
    const React = await import('react');
    const { EnvironmentProvider } = await import('./contexts/EnvironmentContext');
    const { SchedulesPage } = await import('./pages/SchedulesPage');
    return () => (
      <EnvironmentProvider>
        <SchedulesPage />
      </EnvironmentProvider>
    );
  },
  exact: true,
}
```

### Cron builder: `cms/src/plugins/rule-engine/admin/src/components/ScheduleEditor/index.tsx`

**Decision: custom builder, no new npm dep.** Uses `Select`, `TextInput`, `Box`, `RadioGroup`, `Radio` from `@strapi/design-system`. Avoids bundling a separate cron UI library.

```tsx
interface ScheduleEditorProps {
  value: string;            // current cron expression string
  onChange: (expr: string) => void;
}
```

Two modes (toggled by a radio group):

**Preset mode** (default): radio buttons for `@daily` (default), `@hourly`, `@weekly`, `@monthly`, plus a free-form `@every N m/h` input. Selecting a preset assembles and fires `onChange`.

**Custom mode**: Five `Select` dropdowns for Minute / Hour / Day-of-month / Month / Day-of-week. Each dropdown offers "every" (`*`) plus common values (e.g., minute: `0`, `15`, `30`, `45`) plus a "custom…" option that reveals a `TextInput` for ranges/lists. The 5-field string is assembled from the dropdowns and fires `onChange` on each change.

Both modes show:
- A **human-readable description** built by a client-side `describeExpression(expr)` function covering common patterns (e.g., `"@daily"` → "Every day at midnight UTC"; `"0 8 * * 1-5"` → "At 8:00 AM, Monday through Friday"). Unrecognized patterns fall back to "Custom schedule".
- The raw expression string in a read-only code element for copy-paste.

Next-run preview is deferred to the server: `engineNextRunAt` returned from `GET /api/schedules/{id}` is displayed after the schedule is saved — no cron parser in the browser bundle.

**Validation**: when the user enters free text in Custom mode, the component attempts a lightweight client-side check (non-empty, 5 space-separated parts for standard cron, or starts with `@`). Detailed validation happens on the engine at sync time and errors are surfaced via `lastSyncStatus`.

---

## 8. File-by-File Change List

### Engine changes

| File | Action | Description |
|------|--------|-------------|
| `engine/go.mod` | Modify | Add `github.com/robfig/cron/v3 v3.0.1` |
| `engine/go.sum` | Generated | New checksums for robfig/cron |
| `engine/migrations/0004_schedules.up.sql` | Create | `schedules` + `schedule_runs` tables and indexes |
| `engine/migrations/0004_schedules.down.sql` | Create | Drop tables |
| `engine/internal/config/schedule.go` | Create | `Schedule`, `ScheduleRun` structs; `ScheduleStore` interface; run status constants |
| `engine/internal/config/pg_store_schedule.go` | Create | PgStore methods implementing `ScheduleStore`. **Do NOT add `GetActiveFlowByID` here** — it already exists in `pgstore_admin.go` (line 205) and `*config.PgStore` already satisfies `FlowResolver` as-is. Adding it again would be a "method redeclared" compile error. |
| `engine/internal/scheduler/cron.go` | Create | `ParseSchedule`, `NextRun`, `ValidateInterval` wrapping robfig/cron |
| `engine/internal/scheduler/lock.go` | Create | `DistributedLocker` interface + `ValkeyLocker` implementation |
| `engine/internal/scheduler/scheduler.go` | Create | `FlowExecutor` interface; `FlowResolver` interface; `Config` struct; `Scheduler` struct; `New`, `Start`, `Stop`, `fireSchedule` |
| `engine/internal/scheduler/adapters.go` | Create | Local copies of `newTemplatingRegistry` and `newBranchingEvaluator` (from `httpapi/adapters.go`) for use inside `fireSchedule` (avoids circular import) |
| `engine/internal/httpapi/server.go` | Modify | Add `ExecTimeout time.Duration` to the `Deps` struct. Populated in `cmd/engine/main.go` via `execTimeoutFromEnv()`. Used in `admin.go` mount wiring for schedule admin. |
| `engine/internal/httpapi/schedule_admin.go` | Create | `ScheduleAdminStore` interface; `var _ ScheduleAdminStore = (*config.PgStore)(nil)`; `scheduleAdmin` struct with `executor`/`flowStore`/`deps`/`execTimeout` fields; `newScheduleAdmin`; route handlers |
| `engine/internal/httpapi/admin.go` | Modify | Mount schedule routes in `admin.mount()`; add `schedule.read/write` cases to `requireRole` |
| `engine/cmd/engine/main.go` | Modify | Construct and start `Scheduler` in config-store mode; stop on context cancel; wire `execTimeout` and `locker` |

**Test files** (finding 7 fix):

| File | Action | Description |
|------|--------|-------------|
| `engine/internal/scheduler/cron_test.go` | Create | Table-driven tests for `ParseSchedule`, `NextRun`, `ValidateInterval` |
| `engine/internal/scheduler/lock_test.go` | Create | Unit tests for `ValkeyLocker` using a fake Valkey client |
| `engine/internal/scheduler/scheduler_test.go` | Create | Unit tests for `fireSchedule` and the main loop using fakes |
| `engine/internal/config/schedule_test.go` | Create | Struct-field and constant value tests |
| `engine/internal/httpapi/schedule_admin_test.go` | Create | Handler tests using `fakeScheduleAdminStore` + `httptest.NewRecorder` |

### CMS changes

| File | Action | Description |
|------|--------|-------------|
| `cms/src/api/schedule/content-types/schedule/schema.json` | Create | Strapi content type (UID `api::schedule.schedule`) |
| `cms/src/api/schedule/controllers/schedule.ts` | Create | Default Strapi controller stub |
| `cms/src/api/schedule/routes/schedule.ts` | Create | Default Strapi routes stub |
| `cms/src/api/schedule/services/schedule.ts` | Create | Default Strapi service stub |
| `cms/src/publish-middleware.ts` | Modify | Add `'api::schedule.schedule'` to `PUBLISHABLE`; add schedule publish branch |
| `cms/src/plugins/rule-engine/server/src/services/publish-core.ts` | Modify | Add `runSchedulePublish` function |
| `cms/src/plugins/rule-engine/server/src/services/admin-client.ts` | Modify | Add `createSchedule`, `updateSchedule`, `deleteSchedule`, `getSchedule`, `listScheduleRuns` methods |
| `cms/src/plugins/rule-engine/admin/src/pages/SchedulesPage.tsx` | Create | Schedule list page (mirrors WebhooksPage) |
| `cms/src/plugins/rule-engine/admin/src/components/ScheduleEditor/index.tsx` | Create | Cron builder component (preset + custom modes) |
| `cms/src/plugins/rule-engine/admin/src/index.tsx` | Modify | Add `/schedules` route |

**CMS test files:**

| File | Action | Description |
|------|--------|-------------|
| `cms/src/plugins/rule-engine/admin/src/components/ScheduleEditor/ScheduleEditor.test.ts` | Create | Unit tests for cron builder component |

---

## 9. Unit Test Plan

All tests are unit tests (no integration tests).

### `engine/internal/scheduler/cron_test.go`

| Test case | Description |
|-----------|-------------|
| `TestParseSchedule_standard` | `"0 8 * * *"` → parses without error |
| `TestParseSchedule_withRanges` | `"0 8 * * 1-5"` → parses without error |
| `TestParseSchedule_withSteps` | `"*/5 * * * *"` → parses without error |
| `TestParseSchedule_withList` | `"0 8 1,15 * *"` → parses without error |
| `TestParseSchedule_aliases` | `"@hourly"`, `"@daily"`, `"@weekly"`, `"@monthly"` all parse |
| `TestParseSchedule_every` | `"@every 5m"`, `"@every 1h"` parse without error |
| `TestParseSchedule_everyTooFast` | `"@every 30s"` → returns Validation error |
| `TestParseSchedule_invalid` | `"not a cron"` → returns error |
| `TestParseSchedule_sixField` | `"0 0 8 * * *"` → error (6-field not supported) |
| `TestNextRun_dailyAtEight` | `"0 8 * * *"` UTC, from 2026-01-01 00:00 → 2026-01-01 08:00 |
| `TestNextRun_timezone` | `"0 8 * * *"` Asia/Jakarta (UTC+7), from 2026-01-01 00:00 UTC → 2026-01-01 01:00 UTC |
| `TestNextRun_every5min` | `"@every 5m"` → next is exactly +5 minutes |
| `TestValidateInterval_standard` | `"0 8 * * *"` → passes (interval = 24h) |
| `TestValidateInterval_every59s` | `"@every 59s"` → fails |
| `TestValidateInterval_every1m` | `"@every 1m"` → passes |

### `engine/internal/scheduler/lock_test.go`

```go
// fakeValkeyClient records calls and returns canned responses.
type fakeValkeyClient struct { ... }
```

| Test case | Description |
|-----------|-------------|
| `TestTryAcquire_success` | NX returns OK → (true, nil) |
| `TestTryAcquire_alreadyLocked` | NX returns nil (key exists) → (false, nil) |
| `TestTryAcquire_transportError` | Returns connection error → (false, error) |
| `TestRelease_ownerReleases` | Lua CAS: `get` matches → `del` called |
| `TestRelease_nonOwner` | Lua CAS: `get` mismatch → `del` not called, nil error returned |
| `TestRelease_transportError` | Error → logged, nil returned |
| `TestHeartbeat_renewsUntilCancel` | Ticker fires 3 times, all return 1 → cancel stops loop |
| `TestHeartbeat_stopsOnLockLoss` | Lua renew returns 0 → logs warn, returns immediately |

### `engine/internal/scheduler/scheduler_test.go`

```go
// fakeScheduleStore, fakeFlowResolver, fakeFlowExecutor in _test.go files.
```

| Test case | Description |
|-----------|-------------|
| `TestFireSchedule_success` | Fake store + fake executor → run recorded with status=success |
| `TestFireSchedule_lockNotAcquired` | Locker returns (false, nil) → run recorded status=skipped, executor not called |
| `TestFireSchedule_flowNotFound` | FlowResolver returns NotFound → run recorded status=failure |
| `TestFireSchedule_execTimeout` | Executor blocks beyond execTimeout → run recorded status=timeout |
| `TestFireSchedule_nextRunComputed` | After success, UpdateScheduleRunTimes called with correct nextRun |
| `TestFireSchedule_nilLocker` | nil locker → fireSchedule proceeds (single-instance mode) |
| `TestSchedulerLoop_firesDueSchedules` | Two schedules, one due → only due one fires |
| `TestSchedulerLoop_storeError` | ListEnabledSchedules fails → backoff sleep, retry |

### `engine/internal/config/schedule_test.go`

| Test case | Description |
|-----------|-------------|
| `TestScheduleFields` | Schedule struct carries all required fields |
| `TestRunStatusConstants` | Constant string values match spec |
| `TestScheduleRunFields` | ScheduleRun struct carries all required fields |

### `engine/internal/httpapi/schedule_admin_test.go`

Pattern from `webhook_admin_test.go`: `fakeScheduleAdminStore`, table-driven tests over `httptest.NewRecorder`.

| Test case | Description |
|-----------|-------------|
| `TestListSchedules_ok` | 200 + JSON body with schedules array |
| `TestListSchedules_storeErr` | Store error → 502 |
| `TestGetSchedule_ok` | 200 + schedule JSON |
| `TestGetSchedule_notFound` | NotFound → 404 |
| `TestCreateSchedule_ok` | Valid body → 201 |
| `TestCreateSchedule_missingName` | Empty name → 400 |
| `TestCreateSchedule_badCron` | `"not-cron"` → 400 |
| `TestCreateSchedule_badTimezone` | `"Not/A_Zone"` → 400 |
| `TestCreateSchedule_missingFlowId` | Empty flowId → 400 |
| `TestUpdateSchedule_ok` | Partial update → 200 |
| `TestUpdateSchedule_badCron` | Invalid updated cron → 400 |
| `TestDeleteSchedule_ok` | 204 |
| `TestDeleteSchedule_notFound` | 404 |
| `TestTriggerRun_ok` | Fake executor returns nil → 200 + runId |
| `TestTriggerRun_flowNotFound` | FlowResolver returns NotFound → 404 |
| `TestTriggerRun_execTimeout` | Executor returns DeadlineExceeded → 200, status=timeout |
| `TestListRuns_ok` | 200 + runs array |
| `TestListRuns_defaultLimit` | No `?limit` → 20 passed to store |
| `TestScheduleAdmin_requiresStore` | nil store → 500 |
| `TestScheduleAdmin_requiresAuth` | No auth header → guard returns 401 |

### CMS: `cms/src/plugins/rule-engine/admin/src/components/ScheduleEditor/ScheduleEditor.test.ts`

| Test case | Description |
|-----------|-------------|
| `renders preset mode by default` | Preset radios visible; expression starts as `"@daily"` |
| `switching to custom mode shows dropdowns` | Five select elements render |
| `onChange called with assembled expression` | Changing minute select calls onChange with new expression |
| `preset @hourly sets correct expression` | `onChange` called with `"0 * * * *"` |
| `preset @every 5m sets correct expression` | `onChange` called with `"@every 5m"` |
| `invalid custom expression shows error state` | Free-text `"not-cron"` shows inline error |

---

## 10. Resolved Ambiguities

### Lock TTL Policy

**Decision: `lockTTL = 2 × execTimeout`** (default 10 minutes). `execTimeout` defaults to 5 minutes via `SCHEDULER_EXEC_TIMEOUT` env var. Maximum `execTimeout` is 30 minutes (lockTTL capped at 60 minutes). Heartbeat renews at `lockTTL/3` to ensure the lock survives any single heartbeat skip.

### Retention N

**Decision: N = 100 executions per schedule.** Provides ~4 days of history for an hourly schedule and ~3 months for a daily schedule. Prune is atomic with the insert in one transaction.

### Cron Library Choice

**Decision: `github.com/robfig/cron/v3 v3.0.1`.** Parser-only usage; no library goroutines started. BSD-2-Clause license. `go get github.com/robfig/cron/v3@v3.0.1` and pin in `go.mod`.

### Max-Frequency Enforcement

**Decision: enforce at validation time only.** `ValidateInterval` checks `t1 := sched.Next(t0); if t1.Sub(t0) < time.Minute { error }`. Applied in `ParseSchedule`, which is called by both the admin API validators (create/update) and by `fireSchedule`'s next-run computation. Standard 5-field cron cannot produce sub-minute intervals; `@every` expressions are the primary target.

### Scheduler in in-memory mode

**Decision: scheduler does NOT start in in-memory mode.** The `ScheduleStore` methods are only on `*config.PgStore`. `cmd/engine/main.go` wraps scheduler startup behind the same `dsn != ""` gate as config-store mode.

### `POST /admin/schedules/{id}/run` lock behavior

**Decision: no distributed lock for manual runs.** This endpoint is operator-only and is explicitly for testing/debugging. Multiple concurrent manual triggers are the operator's responsibility. The run is recorded with `scheduled_time = now`.

---

## Appendix: `cmd/engine/main.go` wiring changes

```go
// In run(), after buildStore() returns adminStore (config-store mode only):

if dsn := os.Getenv("CONFIG_DSN"); dsn != "" && adminStore != nil {
    var locker scheduler.DistributedLocker
    if valkeyAddr := os.Getenv("VALKEY_ADDR"); valkeyAddr != "" {
        // Reuse the same valkey client opened for the config cache.
        locker = scheduler.NewValkeyLocker(valkeyClient, obsLog)
    } else {
        logger.Warn("scheduler: VALKEY_ADDR not set; running without distributed lock " +
            "(safe for single-instance deployments only)")
    }

    // execTimeoutFromEnv is also used to populate httpapi.Deps.ExecTimeout
    // (called once, shared with the schedule admin HTTP handler).
    execTimeout := execTimeoutFromEnv()

    schedCfg := scheduler.Config{
        Store:        adminStore,           // *config.PgStore satisfies ScheduleStore
        FlowStore:    adminStore,           // *config.PgStore satisfies FlowResolver (pgstore_admin.go:205)
        Executor:     interp,              // *flow.Interpreter satisfies FlowExecutor
        FlowDeps:     flow.Deps{           // inlined — no named helper needed
            Conns:  registry,
            Decide: engine,
            Trace:  tracer,
            Log:    obsLog,
        },
        Locker:       locker,              // nil = single-instance mode
        Log:          obsLog,
        Env:          "",                  // engine logical env (NOT defaultConfigSchema)
        ExecTimeout:  execTimeout,         // SCHEDULER_EXEC_TIMEOUT, default 5m
        PollInterval: 30 * time.Second,
    }
    sched := scheduler.New(schedCfg)
    sched.Start(ctx)
    defer sched.Stop()
}
```

`execTimeoutFromEnv()` parses `SCHEDULER_EXEC_TIMEOUT` (e.g., `"10m"`), defaults to 5 minutes, caps at 30 minutes, and returns a `time.Duration`. It is also assigned to `httpapi.Deps.ExecTimeout` so the schedule admin HTTP handler shares the same timeout value:

```go
// In cmd/engine/main.go:
execTimeout := execTimeoutFromEnv() // parse SCHEDULER_EXEC_TIMEOUT once

// Pass to httpapi.Deps so admin.go can use it in schedule-route wiring:
srv, err := httpapi.NewServer(addr, store, interp, httpapi.Deps{
    // ... existing fields ...
    ExecTimeout: execTimeout,
})

// Also pass to scheduler:
schedCfg := scheduler.Config{
    // ...
    ExecTimeout: execTimeout,
}
```

`schedulerFlowDeps` is defined inline at its single call site (no named helper needed):

```go
schedCfg := scheduler.Config{
    Store:        adminStore,
    FlowStore:    adminStore,
    Executor:     interp,
    FlowDeps:     flow.Deps{Conns: registry, Decide: engine, Trace: tracer, Log: obsLog},
    Locker:       locker,
    Log:          obsLog,
    Env:          "",
    ExecTimeout:  execTimeout,
    PollInterval: 30 * time.Second,
}
```

## Review Findings Response — Rev 1 → Rev 2 (previous review)

| # | Severity | Status | Action taken |
|---|----------|--------|--------------|
| 1 | HIGH | Addressed | Defined `FlowExecutor` interface in `scheduler.go`. Changed `Scheduler` to hold `executor FlowExecutor`. Added `executor FlowExecutor`, `flowStore FlowResolver`, `deps flow.Deps`, `execTimeout time.Duration` to `scheduleAdmin` struct and `newScheduleAdmin` constructor. Wiring in `admin.go mount()` passes `a.interp` as `FlowExecutor`. |
| 2 | HIGH | Addressed | Moved content type to `cms/src/api/schedule/content-types/schedule/schema.json` (UID `api::schedule.schedule`). Added UID to `PUBLISHABLE` in `publish-middleware.ts`. Added `runSchedulePublish` branch to `buildPublishMiddleware`. Removed all lifecycle hook language. |
| 3 | MEDIUM | Addressed | Removed `admin_types.go` from the file list. `var _ ScheduleAdminStore = (*config.PgStore)(nil)` is now placed in `schedule_admin.go`, matching the `webhook_admin.go` precedent. |
| 4 | MEDIUM | Addressed | Appendix wiring now uses `Env: ""` (the engine logical env). `defaultConfigSchema` ("rule_engine") is the Postgres schema name, not the logical env. |
| 5 | MEDIUM | Addressed | `engine/internal/scheduler/adapters.go` added to §8 file list with explicit description of what it contains. |
| 6 | MEDIUM | Addressed | Removed the incorrect opening sentence about `cron.ParseStandard`. §2 now states a single `cron.NewParser(...Descriptor).Parse(expr)` call for all expression types. |
| 7 | NIT | Addressed | All five Go test files and the CMS test file added to §8 under a dedicated Test files sub-table. |
| 8 | NIT | Addressed | `ValidateInterval` pseudocode now uses `t1 := sched.Next(t0); if t1.Sub(t0) < time.Minute { error }` — no misleading `t.Add(-1)`. |
| 9 | NIT | Addressed | `Config` struct defined in §3 with all fields. `Heartbeat` comment updated to: "On lock loss, logs at warn level and returns; caller's execCtx expires naturally." |

---

## Review Findings Response — Rev 2 → Rev 3 (current review)

| # | Severity | Status | Action taken |
|---|----------|--------|--------------|
| 1 | HIGH | Addressed | Removed `GetActiveFlowByID` from the `pg_store_schedule.go` description entirely. Updated the `FlowResolver` interface comment in §3 to state: "`*config.PgStore` already satisfies this interface; `GetActiveFlowByID` is in `pgstore_admin.go` (line 205). No new implementation needed." Updated §8 file list with an explicit "Do NOT add it here" warning. |
| 2 | HIGH | Addressed (Option A) | Added `ExecTimeout time.Duration` to `httpapi.Deps` in `server.go` (added to §8 file list). Fixed `admin.go mount()` wiring to inline `flow.Deps{Conns: a.deps.Conns, Decide: a.deps.Decide, Trace: a.deps.Trace, Log: a.deps.Log}` and use `a.deps.ExecTimeout`. `execTimeoutFromEnv()` stays in `cmd/engine/main.go`; its result is assigned to both `httpapi.Deps.ExecTimeout` and `scheduler.Config.ExecTimeout` at the single call site. |
| 3 | MEDIUM | Addressed | Replaced "mirrors the webhook pattern exactly" with "mirrors the `api::connection.connection` pattern". Added explicit note: "Webhooks do NOT go through this mechanism." Removed the incorrect `'api::webhook.webhook'` entry from the PUBLISHABLE snippet — webhooks are not in that set in the actual codebase. |
| 4 | MEDIUM | Addressed | Both `ScheduleAdminStore` and `FlowResolver` assertions in `admin.go mount()` now use the safe two-value form. A false `ok2` for `FlowResolver` logs a startup warning and skips mounting schedule routes rather than panicking. |
| 5 | NIT | Addressed | Added a multi-line comment to `RunStatusSkipped` documenting the dual-use (normal dedup vs. lock backend error), that the `error` field disambiguates, and that a `RunStatusLockError` constant is a noted v2 improvement. |
| 6 | NIT | Addressed | Removed the `schedulerFlowDeps` named helper. The Appendix now inlines `flow.Deps{Conns: registry, Decide: engine, Trace: tracer, Log: obsLog}` directly at the `scheduler.Config` call site, and shows explicitly how `execTimeout` is shared with `httpapi.Deps`. |
| 7 | NIT | Addressed | Added the rationale for the `[a-z0-9\-_]+` regex to the validation table: "the schedule ID is embedded verbatim in Valkey lock keys; a restricted charset prevents key injection and ensures Redis key-safe characters." Explicitly documented this as an intentional stricter rule vs. the webhook non-empty-only check. |
