package config

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// compile-time assertion that *PgStore satisfies ScheduleStore.
var _ ScheduleStore = (*PgStore)(nil)

// ListEnabledSchedules returns all enabled schedules for env, ordered by next_run ASC.
// This is the scheduler's hot-path method for determining which schedules are due.
func (s *PgStore) ListEnabledSchedules(ctx context.Context, env string) ([]Schedule, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT id, name, schedule, timezone, flow_id, input, enabled, env,
		        last_run, next_run, created_at, created_by, updated_at, updated_by
		   FROM schedules
		  WHERE env=$1 AND enabled=true
		  ORDER BY next_run ASC NULLS LAST`, env)
	if err != nil {
		return nil, classifyPg("list enabled schedules", err)
	}
	defer rows.Close()

	return scanSchedules(rows)
}

// ListSchedules returns all schedules for env (enabled and disabled), ordered by name.
func (s *PgStore) ListSchedules(ctx context.Context, env string) ([]Schedule, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT id, name, schedule, timezone, flow_id, input, enabled, env,
		        last_run, next_run, created_at, created_by, updated_at, updated_by
		   FROM schedules
		  WHERE env=$1
		  ORDER BY name, id`, env)
	if err != nil {
		return nil, classifyPg("list schedules", err)
	}
	defer rows.Close()

	return scanSchedules(rows)
}

// GetSchedule returns a single schedule by id, or NotFound if it doesn't exist.
func (s *PgStore) GetSchedule(ctx context.Context, env, id string) (Schedule, error) {
	pool, err := s.pool(env)
	if err != nil {
		return Schedule{}, err
	}

	var sc Schedule
	var inputRaw []byte
	err = pool.QueryRow(ctx,
		`SELECT id, name, schedule, timezone, flow_id, input, enabled, env,
		        last_run, next_run, created_at, created_by, updated_at, updated_by
		   FROM schedules
		  WHERE env=$1 AND id=$2`, env, id).
		Scan(&sc.ID, &sc.Name, &sc.Schedule, &sc.Timezone, &sc.FlowID,
			&inputRaw, &sc.Enabled, &sc.Env, &sc.LastRun, &sc.NextRun,
			&sc.CreatedAt, &sc.CreatedBy, &sc.UpdatedAt, &sc.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, newErr(NotFound, "schedule not found: "+id)
	}
	if err != nil {
		return Schedule{}, classifyPg("get schedule", err)
	}

	if len(inputRaw) > 0 {
		if err := json.Unmarshal(inputRaw, &sc.Input); err != nil {
			return Schedule{}, wrapErr(Validation, "unmarshal schedule input", err)
		}
	}
	if sc.Input == nil {
		sc.Input = make(map[string]any)
	}

	return sc, nil
}

// CreateSchedule inserts a new schedule. The caller must set ID, Name, Schedule,
// Timezone, FlowID, Input, Enabled, Env, and the audit fields (CreatedBy, UpdatedBy).
// NextRun should be pre-computed by the caller using the cron parser.
func (s *PgStore) CreateSchedule(ctx context.Context, env string, sc Schedule) error {
	pool, err := s.pool(env)
	if err != nil {
		return err
	}

	inputJSON, err := json.Marshal(sc.Input)
	if err != nil {
		return wrapErr(Validation, "marshal schedule input", err)
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO schedules
		   (id, name, schedule, timezone, flow_id, input, enabled, env, next_run, created_by, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		sc.ID, sc.Name, sc.Schedule, sc.Timezone, sc.FlowID,
		inputJSON, sc.Enabled, env, sc.NextRun, sc.CreatedBy, sc.UpdatedBy)
	if err != nil {
		return classifyPg("create schedule", err)
	}
	return nil
}

// UpdateSchedule updates a schedule in place. The caller must provide the full
// Schedule struct with updated fields; the store overwrites all mutable columns.
func (s *PgStore) UpdateSchedule(ctx context.Context, env string, sc Schedule) error {
	pool, err := s.pool(env)
	if err != nil {
		return err
	}

	inputJSON, err := json.Marshal(sc.Input)
	if err != nil {
		return wrapErr(Validation, "marshal schedule input", err)
	}

	tag, err := pool.Exec(ctx,
		`UPDATE schedules
		    SET name=$1, schedule=$2, timezone=$3, flow_id=$4, input=$5,
		        enabled=$6, next_run=$7, updated_at=now(), updated_by=$8
		  WHERE env=$9 AND id=$10`,
		sc.Name, sc.Schedule, sc.Timezone, sc.FlowID, inputJSON,
		sc.Enabled, sc.NextRun, sc.UpdatedBy, env, sc.ID)
	if err != nil {
		return classifyPg("update schedule", err)
	}
	if tag.RowsAffected() == 0 {
		return newErr(NotFound, "schedule not found: "+sc.ID)
	}
	return nil
}

// DeleteSchedule removes a schedule by id. Cascade deletes schedule_runs.
func (s *PgStore) DeleteSchedule(ctx context.Context, env, id string) error {
	pool, err := s.pool(env)
	if err != nil {
		return err
	}

	tag, err := pool.Exec(ctx, `DELETE FROM schedules WHERE env=$1 AND id=$2`, env, id)
	if err != nil {
		return classifyPg("delete schedule", err)
	}
	if tag.RowsAffected() == 0 {
		return newErr(NotFound, "schedule not found: "+id)
	}
	return nil
}

// UpdateScheduleRunTimes is the hot-path method called after each execution:
// updates last_run and next_run in one statement.
func (s *PgStore) UpdateScheduleRunTimes(ctx context.Context, env, id string, lastRun, nextRun time.Time) error {
	pool, err := s.pool(env)
	if err != nil {
		return err
	}

	tag, err := pool.Exec(ctx,
		`UPDATE schedules SET last_run=$1, next_run=$2, updated_at=now() WHERE env=$3 AND id=$4`,
		lastRun, nextRun, env, id)
	if err != nil {
		return classifyPg("update schedule run times", err)
	}
	if tag.RowsAffected() == 0 {
		return newErr(NotFound, "schedule not found: "+id)
	}
	return nil
}

// RecordScheduleRun inserts a run row and prunes rows beyond 100 in the same transaction.
// Returns the new run's ID.
func (s *PgStore) RecordScheduleRun(ctx context.Context, env string, run ScheduleRun) (int64, error) {
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}

	var responseJSON []byte
	if run.Response != nil {
		responseJSON, err = json.Marshal(run.Response)
		if err != nil {
			return 0, wrapErr(Validation, "marshal run response", err)
		}
		// Truncate to 4KB max
		if len(responseJSON) > 4096 {
			responseJSON = responseJSON[:4096]
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, classifyPg("begin record run tx", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var runID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO schedule_runs
		   (schedule_id, scheduled_time, started_at, finished_at, duration_ms, status, error, response)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id`,
		run.ScheduleID, run.ScheduledTime, run.StartedAt, run.FinishedAt,
		run.DurationMs, run.Status, nullIfEmpty(run.Error), responseJSON).Scan(&runID)
	if err != nil {
		return 0, classifyPg("insert schedule run", err)
	}

	// Prune old runs beyond 100 per schedule
	_, err = tx.Exec(ctx,
		`DELETE FROM schedule_runs
		  WHERE schedule_id = $1
		    AND id NOT IN (
		        SELECT id FROM schedule_runs
		         WHERE schedule_id = $1
		         ORDER BY started_at DESC
		         LIMIT 100
		    )`, run.ScheduleID)
	if err != nil {
		return 0, classifyPg("prune schedule runs", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, classifyPg("commit record run tx", err)
	}

	return runID, nil
}

// ListScheduleRuns returns execution logs for a schedule, newest first.
// Limit defaults to 100 if <= 0.
func (s *PgStore) ListScheduleRuns(ctx context.Context, env, scheduleID string, limit int) ([]ScheduleRun, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}

	rows, err := pool.Query(ctx,
		`SELECT id, schedule_id, scheduled_time, started_at, finished_at, duration_ms, status, error, response
		   FROM schedule_runs
		  WHERE schedule_id = $1
		  ORDER BY started_at DESC
		  LIMIT $2`, scheduleID, limit)
	if err != nil {
		return nil, classifyPg("list schedule runs", err)
	}
	defer rows.Close()

	out := make([]ScheduleRun, 0)
	for rows.Next() {
		var run ScheduleRun
		var errStr *string
		var respRaw []byte
		if err := rows.Scan(&run.ID, &run.ScheduleID, &run.ScheduledTime,
			&run.StartedAt, &run.FinishedAt, &run.DurationMs, &run.Status,
			&errStr, &respRaw); err != nil {
			return nil, classifyPg("scan schedule run", err)
		}
		if errStr != nil {
			run.Error = *errStr
		}
		if len(respRaw) > 0 {
			_ = json.Unmarshal(respRaw, &run.Response) // best-effort decode
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate schedule runs", err)
	}
	return out, nil
}

// scanSchedules is a helper to scan schedule rows into a slice.
func scanSchedules(rows pgx.Rows) ([]Schedule, error) {
	out := make([]Schedule, 0)
	for rows.Next() {
		var sc Schedule
		var inputRaw []byte
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Schedule, &sc.Timezone, &sc.FlowID,
			&inputRaw, &sc.Enabled, &sc.Env, &sc.LastRun, &sc.NextRun,
			&sc.CreatedAt, &sc.CreatedBy, &sc.UpdatedAt, &sc.UpdatedBy); err != nil {
			return nil, classifyPg("scan schedule", err)
		}
		if len(inputRaw) > 0 {
			_ = json.Unmarshal(inputRaw, &sc.Input) // best-effort decode
		}
		if sc.Input == nil {
			sc.Input = make(map[string]any)
		}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate schedules", err)
	}
	return out, nil
}
