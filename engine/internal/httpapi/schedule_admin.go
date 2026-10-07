package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/scheduler"
)

// ScheduleAdminStore is the narrow method-set the schedule admin handlers depend on.
// It is satisfied structurally by *config.PgStore.
type ScheduleAdminStore interface {
	ListSchedules(ctx context.Context, env string) ([]config.Schedule, error)
	GetSchedule(ctx context.Context, env, id string) (config.Schedule, error)
	CreateSchedule(ctx context.Context, env string, s config.Schedule) error
	UpdateSchedule(ctx context.Context, env string, s config.Schedule) error
	DeleteSchedule(ctx context.Context, env, id string) error
	ListScheduleRuns(ctx context.Context, env, id string, limit int) ([]config.ScheduleRun, error)
	RecordScheduleRun(ctx context.Context, env string, run config.ScheduleRun) (int64, error)
}

// compile-time assertion that the concrete *config.PgStore satisfies the admin schedule method-set.
var _ ScheduleAdminStore = (*config.PgStore)(nil)

// scheduleAdmin is the control-plane surface for schedule management.
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

// newScheduleAdmin builds the schedule admin surface.
func newScheduleAdmin(
	store ScheduleAdminStore,
	flowStore scheduler.FlowResolver,
	executor scheduler.FlowExecutor,
	deps flow.Deps,
	execTimeout time.Duration,
	guard *auth.OperatorGuard,
	log observ.Logger,
) *scheduleAdmin {
	if execTimeout <= 0 {
		execTimeout = 5 * time.Minute
	}
	return &scheduleAdmin{
		store:       store,
		flowStore:   flowStore,
		executor:    executor,
		deps:        deps,
		execTimeout: execTimeout,
		guard:       guard,
		env:         defaultEnv,
		log:         log,
	}
}

// mount registers the schedule admin routes on the mux.
func (sa *scheduleAdmin) mount(mux *http.ServeMux) {
	h := func(fn http.HandlerFunc) http.Handler { return sa.guard.Protect(fn) }
	mux.Handle("GET /admin/schedules", h(sa.listSchedules))
	mux.Handle("POST /admin/schedules", h(sa.createSchedule))
	mux.Handle("GET /admin/schedules/{id}", h(sa.getSchedule))
	mux.Handle("PUT /admin/schedules/{id}", h(sa.updateSchedule))
	mux.Handle("DELETE /admin/schedules/{id}", h(sa.deleteSchedule))
	mux.Handle("POST /admin/schedules/{id}/run", h(sa.triggerRun))
	mux.Handle("GET /admin/schedules/{id}/runs", h(sa.listRuns))
}

// ---- Request/Response types ----

type createScheduleRequest struct {
	ID       string         `json:"id"`                 // schedule id (caller-assigned)
	Name     string         `json:"name"`               // display name (required)
	Schedule string         `json:"schedule"`           // cron expr (required)
	Timezone string         `json:"timezone,omitempty"` // IANA, defaults to "UTC"
	FlowID   string         `json:"flowId"`             // flow to trigger (required)
	Input    map[string]any `json:"input,omitempty"`    // static flow input
	Enabled  *bool          `json:"enabled,omitempty"`  // defaults to true
	Env      string         `json:"env,omitempty"`      // validated: must be "" or omitted
}

type updateScheduleRequest struct {
	Name     *string         `json:"name,omitempty"`
	Schedule *string         `json:"schedule,omitempty"`
	Timezone *string         `json:"timezone,omitempty"`
	FlowID   *string         `json:"flowId,omitempty"`
	Input    *map[string]any `json:"input,omitempty"`
	Enabled  *bool           `json:"enabled,omitempty"`
}

type scheduleResponse struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Schedule  string         `json:"schedule"`
	Timezone  string         `json:"timezone"`
	FlowID    string         `json:"flowId"`
	Input     map[string]any `json:"input"`
	Enabled   bool           `json:"enabled"`
	LastRun   *time.Time     `json:"lastRun,omitempty"`
	NextRun   *time.Time     `json:"nextRun,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

type triggerRunResponse struct {
	RunID      int64  `json:"runId"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// scheduleIDRegex validates schedule IDs: lowercase alphanumerics, hyphens, underscores.
// This is stricter than webhook IDs because schedule IDs are embedded in Valkey lock keys.
var scheduleIDRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9\-_]*$`)

// ---- Handlers ----

// listSchedules handles GET /admin/schedules.
func (sa *scheduleAdmin) listSchedules(w http.ResponseWriter, r *http.Request) {
	if !sa.requireStore(w) {
		return
	}
	ctx := r.Context()
	schedules, err := sa.store.ListSchedules(ctx, sa.env)
	if err != nil {
		sa.fail(w, ctx, "admin.listSchedules", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": schedules})
}

// getSchedule handles GET /admin/schedules/{id}.
func (sa *scheduleAdmin) getSchedule(w http.ResponseWriter, r *http.Request) {
	if !sa.requireStore(w) {
		return
	}
	scheduleID := r.PathValue("id")
	if scheduleID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing schedule id")
		return
	}

	ctx := r.Context()
	sched, err := sa.store.GetSchedule(ctx, sa.env, scheduleID)
	if err != nil {
		sa.fail(w, ctx, "admin.getSchedule", err)
		return
	}

	writeJSON(w, http.StatusOK, toScheduleResponse(sched))
}

// createSchedule handles POST /admin/schedules.
func (sa *scheduleAdmin) createSchedule(w http.ResponseWriter, r *http.Request) {
	if !sa.requireStore(w) {
		return
	}

	var req createScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Validation
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: id is required")
		return
	}
	if !scheduleIDRegex.MatchString(req.ID) {
		writeError(w, http.StatusBadRequest, "invalid request: id must be lowercase alphanumeric with hyphens/underscores")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid request: name is required")
		return
	}
	if len(req.Name) > 256 {
		writeError(w, http.StatusBadRequest, "invalid request: name must be at most 256 characters")
		return
	}
	if req.Schedule == "" {
		writeError(w, http.StatusBadRequest, "invalid request: schedule is required")
		return
	}
	if req.FlowID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: flowId is required")
		return
	}
	if req.Env != "" && req.Env != sa.env {
		writeError(w, http.StatusBadRequest, "invalid request: env must be empty or match server env")
		return
	}

	// Timezone validation
	timezone := req.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: invalid timezone: "+err.Error())
		return
	}

	// Parse and validate cron expression
	parsedSched, err := scheduler.ParseSchedule(req.Schedule, loc)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	// Compute initial next run time
	nextRun := scheduler.NextRun(parsedSched, time.Now().UTC())

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	input := req.Input
	if input == nil {
		input = make(map[string]any)
	}

	ctx := r.Context()
	actor := actorFromCtx(ctx)

	sched := config.Schedule{
		ID:        req.ID,
		Name:      req.Name,
		Schedule:  req.Schedule,
		Timezone:  timezone,
		FlowID:    req.FlowID,
		Input:     input,
		Enabled:   enabled,
		Env:       sa.env,
		NextRun:   &nextRun,
		CreatedBy: actor,
		UpdatedBy: actor,
	}

	if err := sa.store.CreateSchedule(ctx, sa.env, sched); err != nil {
		sa.fail(w, ctx, "admin.createSchedule", err)
		return
	}

	// Re-fetch to get server-set timestamps.
	created, err := sa.store.GetSchedule(ctx, sa.env, req.ID)
	if err != nil {
		sa.fail(w, ctx, "admin.createSchedule.refetch", err)
		return
	}

	writeJSON(w, http.StatusCreated, toScheduleResponse(created))
}

// updateSchedule handles PUT /admin/schedules/{id}.
func (sa *scheduleAdmin) updateSchedule(w http.ResponseWriter, r *http.Request) {
	if !sa.requireStore(w) {
		return
	}
	scheduleID := r.PathValue("id")
	if scheduleID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing schedule id")
		return
	}

	var req updateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	ctx := r.Context()
	existing, err := sa.store.GetSchedule(ctx, sa.env, scheduleID)
	if err != nil {
		sa.fail(w, ctx, "admin.updateSchedule.fetch", err)
		return
	}

	// Apply updates
	if req.Name != nil {
		if *req.Name == "" {
			writeError(w, http.StatusBadRequest, "invalid request: name cannot be empty")
			return
		}
		if len(*req.Name) > 256 {
			writeError(w, http.StatusBadRequest, "invalid request: name must be at most 256 characters")
			return
		}
		existing.Name = *req.Name
	}
	if req.Timezone != nil {
		_, err := time.LoadLocation(*req.Timezone)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid request: invalid timezone: "+err.Error())
			return
		}
		existing.Timezone = *req.Timezone
	}
	if req.Schedule != nil {
		if *req.Schedule == "" {
			writeError(w, http.StatusBadRequest, "invalid request: schedule cannot be empty")
			return
		}
		loc, _ := time.LoadLocation(existing.Timezone)
		if _, err := scheduler.ParseSchedule(*req.Schedule, loc); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		existing.Schedule = *req.Schedule
	}
	if req.FlowID != nil {
		if *req.FlowID == "" {
			writeError(w, http.StatusBadRequest, "invalid request: flowId cannot be empty")
			return
		}
		existing.FlowID = *req.FlowID
	}
	if req.Input != nil {
		existing.Input = *req.Input
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}

	// Recompute next run time if schedule or timezone changed.
	loc, _ := time.LoadLocation(existing.Timezone)
	parsedSched, _ := scheduler.ParseSchedule(existing.Schedule, loc)
	nextRun := scheduler.NextRun(parsedSched, time.Now().UTC())
	existing.NextRun = &nextRun
	existing.UpdatedBy = actorFromCtx(ctx)

	if err := sa.store.UpdateSchedule(ctx, sa.env, existing); err != nil {
		sa.fail(w, ctx, "admin.updateSchedule", err)
		return
	}

	// Re-fetch to get server-set timestamps.
	updated, err := sa.store.GetSchedule(ctx, sa.env, scheduleID)
	if err != nil {
		sa.fail(w, ctx, "admin.updateSchedule.refetch", err)
		return
	}

	writeJSON(w, http.StatusOK, toScheduleResponse(updated))
}

// deleteSchedule handles DELETE /admin/schedules/{id}.
func (sa *scheduleAdmin) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !sa.requireStore(w) {
		return
	}
	scheduleID := r.PathValue("id")
	if scheduleID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing schedule id")
		return
	}

	ctx := r.Context()
	if err := sa.store.DeleteSchedule(ctx, sa.env, scheduleID); err != nil {
		sa.fail(w, ctx, "admin.deleteSchedule", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// triggerRun handles POST /admin/schedules/{id}/run.
// This endpoint fires the schedule NOW, outside the scheduler loop.
func (sa *scheduleAdmin) triggerRun(w http.ResponseWriter, r *http.Request) {
	if !sa.requireStore(w) {
		return
	}
	scheduleID := r.PathValue("id")
	if scheduleID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing schedule id")
		return
	}

	ctx := r.Context()
	sched, err := sa.store.GetSchedule(ctx, sa.env, scheduleID)
	if err != nil {
		sa.fail(w, ctx, "admin.triggerRun.fetch", err)
		return
	}

	// Resolve the flow.
	fv, err := sa.flowStore.GetActiveFlowByID(ctx, sa.env, sched.FlowID)
	if err != nil {
		sa.fail(w, ctx, "admin.triggerRun.resolveFlow", err)
		return
	}

	startedAt := time.Now().UTC()
	scheduledTime := startedAt // For manual triggers, scheduled == actual.

	// Build trigger input.
	triggerInput := map[string]any{
		"trigger": map[string]any{
			"type":          "schedule",
			"scheduleId":    sched.ID,
			"scheduledTime": scheduledTime.Format(time.RFC3339),
			"actualTime":    startedAt.Format(time.RFC3339),
			"manual":        true,
		},
		"input": sched.Input,
	}
	flowCtx := flow.NewCtx(generateRunID(), "", sa.env, triggerInput)

	// Run with timeout.
	execCtx, cancel := context.WithTimeout(ctx, sa.execTimeout)
	defer cancel()

	execErr := sa.executor.Run(execCtx, &fv.Tree, flow.Version{FlowID: fv.FlowID, Version: fv.Version}, flowCtx, sa.deps)

	finishedAt := time.Now().UTC()
	durationMs := finishedAt.Sub(startedAt).Milliseconds()

	status := config.RunStatusSuccess
	var errStr string
	if execErr != nil {
		status = config.RunStatusFailure
		if errors.Is(execErr, context.DeadlineExceeded) {
			status = config.RunStatusTimeout
		}
		errStr = execErr.Error()
	}

	// Record the run.
	run := config.ScheduleRun{
		ScheduleID:    sched.ID,
		ScheduledTime: scheduledTime,
		StartedAt:     startedAt,
		FinishedAt:    &finishedAt,
		DurationMs:    &durationMs,
		Status:        status,
		Error:         errStr,
		Response:      flowCtx.Response,
	}
	runID, recordErr := sa.store.RecordScheduleRun(ctx, sa.env, run)
	if recordErr != nil && sa.log != nil {
		sa.log.Emit(ctx, "warn", "admin.triggerRun: record failed", map[string]any{
			"schedule_id": sched.ID,
			"err":         recordErr.Error(),
		})
	}

	writeJSON(w, http.StatusOK, triggerRunResponse{
		RunID:      runID,
		Status:     status,
		DurationMs: durationMs,
		Error:      errStr,
	})
}

// listRuns handles GET /admin/schedules/{id}/runs.
func (sa *scheduleAdmin) listRuns(w http.ResponseWriter, r *http.Request) {
	if !sa.requireStore(w) {
		return
	}
	scheduleID := r.PathValue("id")
	if scheduleID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing schedule id")
		return
	}

	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	ctx := r.Context()
	runs, err := sa.store.ListScheduleRuns(ctx, sa.env, scheduleID, limit)
	if err != nil {
		sa.fail(w, ctx, "admin.listRuns", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// ---- Helpers ----

func (sa *scheduleAdmin) requireStore(w http.ResponseWriter) bool {
	if sa.store == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule admin not available")
		return false
	}
	return true
}

func (sa *scheduleAdmin) fail(w http.ResponseWriter, ctx context.Context, label string, err error) {
	if sa.log != nil {
		sa.log.Emit(ctx, "error", label, map[string]any{"err": err.Error()})
	}
	code, msg := statusForAdmin(err)
	writeError(w, code, msg)
}

func toScheduleResponse(s config.Schedule) scheduleResponse {
	return scheduleResponse{
		ID:        s.ID,
		Name:      s.Name,
		Schedule:  s.Schedule,
		Timezone:  s.Timezone,
		FlowID:    s.FlowID,
		Input:     s.Input,
		Enabled:   s.Enabled,
		LastRun:   s.LastRun,
		NextRun:   s.NextRun,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
}

func generateRunID() string {
	return "run-" + time.Now().Format("20060102-150405")
}

func actorFromCtx(ctx context.Context) string {
	if op, ok := auth.OperatorFrom(ctx); ok {
		return op.Subject
	}
	return "admin"
}

// auditCtx stamps the authenticated operator subject as the audit actor.
func (sa *scheduleAdmin) auditCtx(ctx context.Context) context.Context {
	if op, ok := auth.OperatorFrom(ctx); ok {
		return config.WithAuditActor(ctx, op.Subject)
	}
	return ctx
}
