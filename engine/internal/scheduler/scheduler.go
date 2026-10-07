package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
)

// FlowExecutor is the interpreter seam the scheduler uses to run a flow.
// Satisfied by *flow.Interpreter. Defined here so the scheduler and scheduleAdmin
// are independently testable with a fake.
type FlowExecutor interface {
	Run(ctx context.Context, node *flow.Node, ver flow.Version, c *flow.Ctx, deps flow.Deps) error
}

// FlowResolver looks up an active flow version by flow ID (not by HTTP route).
// *config.PgStore already satisfies this interface via GetActiveFlowByID.
type FlowResolver interface {
	GetActiveFlowByID(ctx context.Context, env, flowID string) (config.FlowVersion, error)
}

// Config holds the constructor options for a Scheduler.
type Config struct {
	Store        config.ScheduleStore
	FlowStore    FlowResolver
	Executor     FlowExecutor      // *flow.Interpreter satisfies this
	FlowDeps     flow.Deps         // conns, decide, trace, log (not request-scoped)
	Locker       DistributedLocker // nil = single-instance mode (no Valkey)
	Log          observ.Logger
	Env          string        // engine logical env (always "" in v1)
	ExecTimeout  time.Duration // default 5m; overridden by SCHEDULER_EXEC_TIMEOUT
	PollInterval time.Duration // default 30s; how often to reload schedule list from DB
}

// Scheduler is the background service that fires flows on a time-based schedule.
// It is started via Start() and stopped via Stop().
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
	wg           sync.WaitGroup
}

// New builds a Scheduler with the given configuration.
func New(cfg Config) *Scheduler {
	if cfg.ExecTimeout <= 0 {
		cfg.ExecTimeout = 5 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
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

// Start begins the scheduler loop in a background goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	if s.locker == nil && s.log != nil {
		s.log.Emit(ctx, "warn", "scheduler running without distributed lock (VALKEY_ADDR not set); safe for single-instance deployments only", nil)
	}

	go func() {
		defer close(s.doneCh)
		s.loop(ctx)
	}()
}

// Stop signals the scheduler to stop and waits for it to exit.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	<-s.doneCh
	s.wg.Wait()
}

// loop is the main scheduler loop.
func (s *Scheduler) loop(ctx context.Context) {
	backoff := time.Second

	for {
		schedules, err := s.store.ListEnabledSchedules(ctx, s.env)
		if err != nil {
			if s.log != nil {
				s.log.Emit(ctx, "warn", "scheduler: load schedules failed", map[string]any{"err": err.Error()})
			}
			select {
			case <-time.After(min(backoff, 30*time.Second)):
				backoff = min(backoff*2, 30*time.Second)
			case <-s.stopCh:
				return
			case <-ctx.Done():
				return
			}
			continue
		}
		backoff = time.Second

		now := time.Now().UTC()
		var nextDue *time.Time

		for _, sched := range schedules {
			// Fire if due (with 100ms grace window for timing jitter).
			if sched.NextRun != nil && sched.NextRun.Before(now.Add(100*time.Millisecond)) {
				s.wg.Add(1)
				go func(sc config.Schedule, scheduledTime time.Time) {
					defer s.wg.Done()
					s.fireSchedule(ctx, sc, scheduledTime)
				}(sched, *sched.NextRun)
			} else if sched.NextRun != nil {
				// Track nearest upcoming schedule.
				if nextDue == nil || sched.NextRun.Before(*nextDue) {
					nextDue = sched.NextRun
				}
			}
		}

		// Sleep until next due schedule or pollInterval, whichever is sooner.
		var sleepDur time.Duration
		if nextDue != nil {
			sleepDur = time.Until(*nextDue)
			if sleepDur > s.pollInterval {
				sleepDur = s.pollInterval
			}
		} else {
			sleepDur = s.pollInterval
		}
		if sleepDur < 0 {
			sleepDur = 0
		}

		select {
		case <-time.After(sleepDur):
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// fireSchedule executes a single schedule run with distributed locking.
func (s *Scheduler) fireSchedule(ctx context.Context, sched config.Schedule, scheduledTime time.Time) {
	execCtx, cancel := context.WithTimeout(ctx, s.execTimeout)
	defer cancel()

	lockKey := "schedule:" + sched.ID + ":lock"
	lockToken := uuid.New().String()
	lockTTL := 2 * s.execTimeout

	// Distributed lock: skip if another instance owns it.
	if s.locker != nil {
		acquired, err := s.locker.TryAcquire(execCtx, lockKey, lockToken, lockTTL)
		if err != nil {
			if s.log != nil {
				s.log.Emit(execCtx, "warn", "scheduler: lock acquire error", map[string]any{
					"schedule_id": sched.ID,
					"err":         err.Error(),
				})
			}
			// Fail open: record skipped and return.
			s.recordRun(ctx, sched, scheduledTime, time.Now().UTC(), config.RunStatusSkipped, err, nil)
			return
		}
		if !acquired {
			// Another instance owns the lock — normal contention, skip silently.
			s.recordRun(ctx, sched, scheduledTime, time.Now().UTC(), config.RunStatusSkipped, nil, nil)
			return
		}
		defer s.locker.Release(ctx, lockKey, lockToken)

		// Start heartbeat goroutine.
		heartbeatCtx, stopHeartbeat := context.WithCancel(execCtx)
		defer stopHeartbeat()
		go s.locker.Heartbeat(heartbeatCtx, lockKey, lockToken, lockTTL)
	}

	startedAt := time.Now().UTC()

	// Resolve the active flow by ID.
	fv, err := s.flowStore.GetActiveFlowByID(execCtx, s.env, sched.FlowID)
	if err != nil {
		s.finishRun(ctx, sched, scheduledTime, startedAt, config.RunStatusFailure, err, nil)
		return
	}

	// Build trigger input following the spec's required shape.
	triggerInput := map[string]any{
		"trigger": map[string]any{
			"type":          "schedule",
			"scheduleId":    sched.ID,
			"scheduledTime": scheduledTime.Format(time.RFC3339),
			"actualTime":    startedAt.Format(time.RFC3339),
		},
		"input": sched.Input,
	}
	flowCtx := flow.NewCtx(generateRunID(), "", s.env, triggerInput)

	// Run the flow.
	execErr := s.executor.Run(execCtx, &fv.Tree, flow.Version{FlowID: fv.FlowID, Version: fv.Version}, flowCtx, s.deps)

	status := config.RunStatusSuccess
	if execErr != nil {
		status = config.RunStatusFailure
		if errors.Is(execErr, context.DeadlineExceeded) {
			status = config.RunStatusTimeout
		}
	}

	s.finishRun(ctx, sched, scheduledTime, startedAt, status, execErr, flowCtx.Response)
}

// finishRun records the run and updates the schedule's run times.
func (s *Scheduler) finishRun(ctx context.Context, sched config.Schedule, scheduledTime, startedAt time.Time, status string, execErr error, response map[string]any) {
	now := time.Now().UTC()
	durationMs := now.Sub(startedAt).Milliseconds()

	// Compute next run time in the schedule's timezone.
	loc, _ := time.LoadLocation(sched.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	parsed, parseErr := ParseSchedule(sched.Schedule, loc)
	var nextRun time.Time
	if parseErr == nil {
		nextRun = NextRun(parsed, scheduledTime)
	} else {
		// Fallback: use current time if parse fails (shouldn't happen for validated schedules).
		nextRun = NextRun(parsed, time.Now().UTC())
	}

	// Persist results (best-effort: log failures, don't panic).
	if err := s.store.UpdateScheduleRunTimes(ctx, s.env, sched.ID, startedAt, nextRun); err != nil && s.log != nil {
		s.log.Emit(ctx, "warn", "scheduler: update run times failed", map[string]any{
			"schedule_id": sched.ID,
			"err":         err.Error(),
		})
	}

	run := config.ScheduleRun{
		ScheduleID:    sched.ID,
		ScheduledTime: scheduledTime,
		StartedAt:     startedAt,
		FinishedAt:    &now,
		DurationMs:    &durationMs,
		Status:        status,
		Error:         safeErrStr(execErr),
		Response:      truncateResponse(response, 4096),
	}
	if _, err := s.store.RecordScheduleRun(ctx, s.env, run); err != nil && s.log != nil {
		s.log.Emit(ctx, "warn", "scheduler: record run failed", map[string]any{
			"schedule_id": sched.ID,
			"err":         err.Error(),
		})
	}
}

// recordRun is a simpler version of finishRun for skipped runs (no execution).
func (s *Scheduler) recordRun(ctx context.Context, sched config.Schedule, scheduledTime, startedAt time.Time, status string, execErr error, response map[string]any) {
	run := config.ScheduleRun{
		ScheduleID:    sched.ID,
		ScheduledTime: scheduledTime,
		StartedAt:     startedAt,
		Status:        status,
		Error:         safeErrStr(execErr),
	}
	if _, err := s.store.RecordScheduleRun(ctx, s.env, run); err != nil && s.log != nil {
		s.log.Emit(ctx, "warn", "scheduler: record skipped run failed", map[string]any{
			"schedule_id": sched.ID,
			"err":         err.Error(),
		})
	}
}

// generateRunID creates a unique run ID for tracing.
func generateRunID() string {
	return uuid.New().String()
}

// safeErrStr converts an error to string, returning empty string for nil.
func safeErrStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// truncateResponse truncates the response map to maxBytes when serialized.
// This is a simple implementation that just returns the response as-is;
// the actual truncation happens in RecordScheduleRun.
func truncateResponse(resp map[string]any, maxBytes int) any {
	if resp == nil || len(resp) == 0 {
		return nil
	}
	return resp
}
