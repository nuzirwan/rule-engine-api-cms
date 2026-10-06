# TASK-008: Scheduled Triggers (Cron)

## Summary
Add cron-like scheduler that triggers flows on a schedule (daily reports, periodic data sync, health checks).

## Context
- Current triggers: HTTP request, webhooks (if TASK-003 built)
- Missing: time-based triggers
- Use cases: daily reports, hourly sync, periodic cleanup

## Requirements

### 1. Schedule Configuration
```json
{
  "id": "daily-report",
  "name": "Daily Sales Report",
  "schedule": "0 8 * * *",           // cron expression: 8 AM daily
  "timezone": "Asia/Jakarta",
  "flowId": "generate-sales-report",
  "input": {                          // static input for the flow
    "reportType": "daily",
    "recipients": ["sales@example.com"]
  },
  "enabled": true,
  "env": "",
  "lastRun": "2026-10-04T08:00:00Z",
  "nextRun": "2026-10-05T08:00:00Z"
}
```

### 2. Cron Expression Support
Standard cron format: `minute hour day month weekday`
- `* * * * *` — every minute
- `0 * * * *` — every hour
- `0 8 * * *` — daily at 8 AM
- `0 8 * * 1-5` — weekdays at 8 AM
- `0 0 1 * *` — first of month at midnight

Optional: human-readable aliases
- `@hourly`, `@daily`, `@weekly`, `@monthly`
- `@every 5m`, `@every 1h`

### 3. Scheduler Service
- Background goroutine in engine
- Loads schedules from config store
- Calculates next run time
- Triggers flow at scheduled time
- Records execution result

### 4. Execution Context
When schedule triggers a flow, context includes:
```json
{
  "trigger": {
    "type": "schedule",
    "scheduleId": "daily-report",
    "scheduledTime": "2026-10-05T08:00:00Z",
    "actualTime": "2026-10-05T08:00:01Z"
  },
  "input": { /* from schedule config */ }
}
```

### 5. Schedule Admin API
```
GET  /admin/schedules           — list all schedules
GET  /admin/schedules/{id}      — get schedule + last/next run
POST /admin/schedules           — create schedule
PUT  /admin/schedules/{id}      — update schedule
DELETE /admin/schedules/{id}    — delete schedule
POST /admin/schedules/{id}/run  — trigger immediately (for testing)
GET  /admin/schedules/{id}/runs — execution history
```

### 6. Execution History
Store last N executions per schedule:
- Timestamp
- Duration
- Status (success/failure)
- Error message (if failed)
- Flow response summary

### 7. Distributed Locking
For multi-instance deployments:
- Use Valkey for distributed lock
- Only one instance executes each schedule
- Lock key: `schedule:{id}:lock`
- Lock TTL: 2x expected execution time

## Files to Modify/Create

### Engine (`engine/`)

#### Config Layer
- `internal/config/schedule.go` — Schedule struct, store methods
- `internal/config/pg_store_schedule.go` — Postgres implementation
- `internal/config/migrations/005_schedules.sql` — schedule tables

#### Scheduler Service
- `internal/scheduler/scheduler.go` — main scheduler loop
- `internal/scheduler/cron.go` — cron expression parser
- `internal/scheduler/lock.go` — distributed lock via Valkey

#### HTTP Layer
- `internal/httpapi/schedule_admin.go` — admin CRUD handlers

#### Wiring
- `cmd/engine/main.go` — start scheduler goroutine

### CMS (`cms/src/plugins/rule-engine/`)
- `server/content-types/schedule/schema.json` — Schedule content type
- `admin/src/pages/SchedulesPage.tsx` — schedule management UI
- `admin/src/components/ScheduleEditor/index.tsx` — schedule editor with cron builder

## Acceptance Criteria
- [ ] Schedules stored in config store
- [ ] Scheduler triggers flows at correct times
- [ ] Timezone handling works correctly
- [ ] Distributed lock prevents duplicate execution
- [ ] Admin CRUD endpoints work
- [ ] Execution history recorded
- [ ] "Run Now" triggers immediate execution
- [ ] CMS can create/edit schedules

## Testing
- Unit tests for cron expression parsing
- Unit tests for next run calculation
- Unit tests for distributed lock
- Integration test: schedule flow, wait for trigger, verify execution
- Integration test: multi-instance, verify only one executes

## Cron Expression Examples
| Expression | Description |
|------------|-------------|
| `* * * * *` | Every minute |
| `*/5 * * * *` | Every 5 minutes |
| `0 * * * *` | Every hour at :00 |
| `0 8 * * *` | Daily at 8:00 AM |
| `0 8 * * 1-5` | Weekdays at 8:00 AM |
| `0 0 * * 0` | Sundays at midnight |
| `0 0 1 * *` | First of month |
| `0 0 1 1 *` | January 1st (yearly) |

## Security Considerations
- Schedules require operator auth to create/modify
- Maximum frequency limit (e.g., no faster than every 1 minute)
- Execution timeout to prevent runaway flows
- Rate limit on total scheduled executions per minute
