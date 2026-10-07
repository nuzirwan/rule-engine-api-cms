package scheduler

import (
	"errors"
	"testing"
	"time"
)

func TestParseSchedule_ValidExpressions(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"5-field cron", "0 9 * * *"},           // 9am daily
		{"every hour", "@hourly"},               // every hour
		{"every day", "@daily"},                 // every day at midnight
		{"every week", "@weekly"},               // every Sunday
		{"every month", "@monthly"},             // first of month
		{"every 5 minutes", "*/5 * * * *"},      // every 5 min
		{"every 1 minute", "* * * * *"},         // every 1 min (minimum allowed)
		{"specific minutes", "0,30 * * * *"},    // on the hour and half-hour
		{"weekdays at 9am", "0 9 * * 1-5"},      // Mon-Fri 9am
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := ParseSchedule(tt.expr, time.UTC)
			if err != nil {
				t.Fatalf("ParseSchedule(%q) = err %v, want success", tt.expr, err)
			}
			if sched == nil {
				t.Fatalf("ParseSchedule(%q) = nil schedule, want non-nil", tt.expr)
			}
		})
	}
}

func TestParseSchedule_InvalidExpressions(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"empty", ""},
		{"gibberish", "not a cron expression"},
		{"too few fields", "* * *"},
		{"invalid field values", "99 99 99 99 99"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSchedule(tt.expr, time.UTC)
			if err == nil {
				t.Fatalf("ParseSchedule(%q) = nil error, want error", tt.expr)
			}
			if !errors.Is(err, ErrInvalidSchedule) {
				t.Fatalf("ParseSchedule(%q) error = %v, want ErrInvalidSchedule", tt.expr, err)
			}
		})
	}
}

func TestParseSchedule_SubMinuteRejected(t *testing.T) {
	// @every 30s should be rejected (less than 1 minute interval).
	_, err := ParseSchedule("@every 30s", time.UTC)
	if err == nil {
		t.Fatal("ParseSchedule(@every 30s) = nil error, want error for sub-minute interval")
	}
	if !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("error = %v, want ErrInvalidSchedule", err)
	}
}

func TestParseSchedule_EveryMinuteAllowed(t *testing.T) {
	// @every 1m should be allowed (exactly 1 minute interval).
	sched, err := ParseSchedule("@every 1m", time.UTC)
	if err != nil {
		t.Fatalf("ParseSchedule(@every 1m) = err %v, want success", err)
	}
	if sched == nil {
		t.Fatal("ParseSchedule(@every 1m) = nil schedule, want non-nil")
	}
}

func TestNextRun(t *testing.T) {
	sched, err := ParseSchedule("0 9 * * *", time.UTC) // 9am daily
	if err != nil {
		t.Fatalf("ParseSchedule = err %v", err)
	}

	// Start from 8am Jan 1 2026 — next run should be 9am same day.
	from := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	next := NextRun(sched, from)
	want := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", next, want)
	}

	// Start from 10am Jan 1 — next run should be 9am Jan 2.
	from2 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	next2 := NextRun(sched, from2)
	want2 := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Fatalf("NextRun = %v, want %v", next2, want2)
	}
}

func TestParseSchedule_Timezone(t *testing.T) {
	// Schedule runs at 9am in Asia/Jakarta (UTC+7).
	jakarta, _ := time.LoadLocation("Asia/Jakarta")
	sched, err := ParseSchedule("0 9 * * *", jakarta)
	if err != nil {
		t.Fatalf("ParseSchedule = err %v", err)
	}

	// From midnight UTC Jan 1 2026, next 9am Jakarta is 9am Jakarta = 2am UTC.
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next := NextRun(sched, from)

	// 9am Jakarta = 2am UTC on the same day.
	wantUTC := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	if !next.Equal(wantUTC) {
		t.Fatalf("NextRun = %v, want %v (9am Jakarta = 2am UTC)", next, wantUTC)
	}
}
