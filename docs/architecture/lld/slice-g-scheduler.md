# LLD — Slice G: Scheduler (time-based flow execution)

Package: `internal/scheduler` · Module: `nzr-rules-engine` · Go 1.22+
Status: **implemented** · Last updated: 2026-10-07

The scheduler package provides background time-based flow execution, enabling flows to run on
cron schedules or at fixed intervals rather than only in response to HTTP requests.

---

## 1. Package responsibilities

`internal/scheduler` is the home for:

- **Scheduler service** — long-running background goroutine that checks for due flows and
  executes them.
- **Distributed locking** — prevents duplicate executions across multiple engine instances.
- **Cron parsing** — standard cron expression support (`ParseSchedule`, `NextRun`).
- **Schedule validation** — `ValidateInterval` ensures schedules are within acceptable bounds.

### Explicit non-responsibilities

- Flow execution mechanics → Slice A (`internal/flow`)
- Config storage → Slice D (`internal/config`)
- HTTP endpoints → Slice E/F (`internal/httpapi`)

---

## 2. Core types

```go
// Scheduler manages scheduled flow executions.
type Scheduler struct {
    cfg          Config
    locker       DistributedLocker
    executor     FlowExecutor
    resolver     FlowResolver
    // ...
}

// Config holds scheduler configuration.
type Config struct {
    PollInterval time.Duration // how often to check for due flows
    LockTTL      time.Duration // distributed lock time-to-live
    // ...
}

// DistributedLocker prevents duplicate runs across instances.
type DistributedLocker interface {
    // TryLock attempts to acquire a lock for the given key.
    // Returns true if acquired, false if already held by another instance.
    TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
    // Unlock releases a previously acquired lock.
    Unlock(ctx context.Context, key string) error
}

// FlowExecutor runs a flow (implemented by the interpreter).
type FlowExecutor interface {
    Execute(ctx context.Context, flowID string, input map[string]any) error
}

// FlowResolver retrieves scheduled flow definitions.
type FlowResolver interface {
    ScheduledFlows(ctx context.Context) ([]ScheduledFlow, error)
}

// ScheduledFlow represents a flow with its schedule.
type ScheduledFlow struct {
    FlowID   string
    Schedule string // cron expression
    Enabled  bool
    LastRun  time.Time
    NextRun  time.Time
}
```

---

## 3. Scheduler lifecycle

```go
// Start begins the scheduler loop (non-blocking).
func (s *Scheduler) Start(ctx context.Context) error

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop(ctx context.Context) error
```

### Main loop (pseudocode)

```
func (s *Scheduler) loop(ctx context.Context):
    ticker := time.NewTicker(s.cfg.PollInterval)
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            flows := s.resolver.ScheduledFlows(ctx)
            for _, flow := range flows:
                if flow.Enabled && time.Now().After(flow.NextRun):
                    // Distributed lock prevents duplicate execution
                    lockKey := fmt.Sprintf("scheduler:%s", flow.FlowID)
                    if acquired := s.locker.TryLock(ctx, lockKey, s.cfg.LockTTL); acquired:
                        go s.executeFlow(ctx, flow)
        }
    }
```

---

## 4. Cron parsing

Standard cron expression support:

```go
// ParseSchedule parses a cron expression and returns the parsed schedule.
// Supports: minute hour day-of-month month day-of-week
// Example: "0 */2 * * *" (every 2 hours at minute 0)
func ParseSchedule(expr string) (*Schedule, error)

// NextRun calculates the next execution time from a reference time.
func (s *Schedule) NextRun(from time.Time) time.Time

// ValidateInterval ensures the schedule interval is within bounds.
// Rejects schedules that would run more frequently than minInterval.
func ValidateInterval(expr string, minInterval time.Duration) error
```

---

## 5. File structure

```
internal/scheduler/
  scheduler.go      // Scheduler struct, Start/Stop/loop
  cron.go           // ParseSchedule, NextRun, ValidateInterval
  cron_test.go      // cron parsing tests
```

---

## 6. Acceptance criteria

| AC | Description |
|----|-------------|
| AC-33 | Scheduler executes flows on cron schedule |
| | - A flow with schedule `"*/5 * * * *"` runs every 5 minutes |
| | - Only one instance runs the flow (distributed lock) |
| | - Missed runs (instance down) execute on next poll |

---

## 7. Verification (2026-10-07)

```bash
ls engine/internal/scheduler/
# Output: cron.go cron_test.go scheduler.go

grep -n "type Scheduler struct" engine/internal/scheduler/scheduler.go
# Confirms: Scheduler struct with Start/Stop methods

grep -n "ParseSchedule\|NextRun" engine/internal/scheduler/cron.go
# Confirms: cron parsing functions exist
```
