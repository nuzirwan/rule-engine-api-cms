// Package scheduler implements the cron-based flow scheduler.
package scheduler

import (
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// ErrInvalidSchedule is returned when a cron expression is invalid or violates
// the minimum interval constraint.
var ErrInvalidSchedule = errors.New("invalid schedule")

// cronParser is the single parser instance covering 5-field standard cron,
// @-aliases (@hourly, @daily, etc.), and @every directives.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ParseSchedule parses a cron expression or @-alias into a robfig/cron Schedule.
// It uses a single parser instance that covers 5-field standard cron, @-aliases,
// and @every directives. The max-frequency gate (≥ 1 minute) is enforced here via
// ValidateInterval. Returns ErrInvalidSchedule on any failure.
func ParseSchedule(expr string, loc *time.Location) (cron.Schedule, error) {
	if loc == nil {
		loc = time.UTC
	}
	// robfig/cron parses in UTC by default; timezone is applied via withLocation
	// wrapper below.
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchedule, err)
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
		return fmt.Errorf("%w: interval must be at least 1 minute", ErrInvalidSchedule)
	}
	return nil
}

// locSchedule wraps a Schedule to shift Next() results into loc.
// robfig/cron does not expose a timezone option on the parser directly;
// the standard approach is to wrap the schedule.
type locSchedule struct {
	inner cron.Schedule
	loc   *time.Location
}

func (l locSchedule) Next(t time.Time) time.Time {
	return l.inner.Next(t.In(l.loc))
}

func withLocation(s cron.Schedule, loc *time.Location) cron.Schedule {
	if loc == time.UTC {
		return s
	}
	return locSchedule{inner: s, loc: loc}
}
