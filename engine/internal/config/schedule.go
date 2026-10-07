package config

import (
	"context"
	"time"
)

// Schedule is a cron-based trigger configuration. Unlike flows/webhooks, schedules
// are mutable (updated in-place, no immutable version history).
type Schedule struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Schedule  string         `json:"schedule"`            // cron expr or @alias
	Timezone  string         `json:"timezone"`            // IANA, e.g. "Asia/Jakarta"
	FlowID    string         `json:"flowId"`
	Input     map[string]any `json:"input"`               // static flow input
	Enabled   bool           `json:"enabled"`
	Env       string         `json:"env"`
	LastRun   *time.Time     `json:"lastRun,omitempty"`
	NextRun   *time.Time     `json:"nextRun,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	CreatedBy string         `json:"createdBy"`
	UpdatedAt time.Time      `json:"updatedAt"`
	UpdatedBy string         `json:"updatedBy"`
}

// ScheduleRun records one execution attempt for audit and debug.
type ScheduleRun struct {
	ID            int64      `json:"id"`
	ScheduleID    string     `json:"scheduleId"`
	ScheduledTime time.Time  `json:"scheduledTime"` // when it was supposed to fire
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	DurationMs    *int64     `json:"durationMs,omitempty"`
	Status        string     `json:"status"` // "success"|"failure"|"timeout"|"skipped"
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
	// mean this instance did not execute the flow.
	RunStatusSkipped = "skipped"
)

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
