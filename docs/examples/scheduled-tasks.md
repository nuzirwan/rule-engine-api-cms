# Scheduled Tasks Reference — cron job configuration

The rule engine supports scheduled task execution using cron expressions. Tasks
trigger flows at specified intervals with optional input parameters. This guide
covers scheduling configuration and management.

**Related architecture:** [slice-g-scheduler.md](../architecture/lld/slice-g-scheduler.md)

## Overview

Scheduled tasks trigger flows at specified times:

```
Cron Expression → Scheduler → Distributed Lock → Flow Execution
```

The scheduler:
- Evaluates cron expressions to determine next run times
- Acquires distributed locks to ensure single execution across instances
- Triggers the configured flow with provided input
- Records execution history and status

---

## Schedule Configuration

```json
{
  "id": "daily-report",
  "name": "Daily Sales Report",
  "schedule": "0 8 * * *",
  "timezone": "Asia/Jakarta",
  "flowId": "generate-report",
  "input": {
    "reportType": "daily",
    "format": "pdf"
  },
  "enabled": true,
  "env": "production"
}
```

| Field      | Type    | Description                              |
|------------|---------|------------------------------------------|
| `id`       | string  | Unique schedule identifier               |
| `name`     | string  | Human-readable name                      |
| `schedule` | string  | Cron expression or @-alias               |
| `timezone` | string  | IANA timezone (default: UTC)             |
| `flowId`   | string  | Flow to trigger                          |
| `input`    | object  | Static input passed to flow              |
| `enabled`  | bool    | Whether schedule is active               |
| `env`      | string  | Environment                              |

---

## Cron Format

Standard 5-field cron format:

```
┌───────────── minute (0-59)
│ ┌───────────── hour (0-23)
│ │ ┌───────────── day of month (1-31)
│ │ │ ┌───────────── month (1-12)
│ │ │ │ ┌───────────── day of week (0-6, Sunday=0)
│ │ │ │ │
* * * * *
```

### Examples

| Expression      | Description                           |
|-----------------|---------------------------------------|
| `0 * * * *`     | Every hour at minute 0                |
| `*/15 * * * *`  | Every 15 minutes                      |
| `0 8 * * *`     | Daily at 8:00 AM                      |
| `0 8 * * 1-5`   | Weekdays at 8:00 AM                   |
| `0 0 1 * *`     | First day of month at midnight        |
| `0 0 * * 0`     | Every Sunday at midnight              |

### @-Aliases

| Alias       | Equivalent     | Description       |
|-------------|----------------|-------------------|
| `@hourly`   | `0 * * * *`    | Every hour        |
| `@daily`    | `0 0 * * *`    | Daily at midnight |
| `@weekly`   | `0 0 * * 0`    | Weekly on Sunday  |
| `@monthly`  | `0 0 1 * *`    | First of month    |

### @every Shorthand

For interval-based schedules:

| Expression    | Description        |
|---------------|--------------------|
| `@every 5m`   | Every 5 minutes    |
| `@every 1h`   | Every hour         |
| `@every 30s`  | Every 30 seconds   |
| `@every 2h30m`| Every 2.5 hours    |

**Minimum interval**: 1 minute (enforced by validation)

---

## Scheduler Settings

| Setting        | Default | Env Override             |
|----------------|---------|--------------------------|
| `ExecTimeout`  | 5m      | `SCHEDULER_EXEC_TIMEOUT` |
| `PollInterval` | 30s     | -                        |

The scheduler polls for due tasks every `PollInterval` and times out executions
after `ExecTimeout`.

---

## Distributed Locking

The scheduler uses Valkey for distributed deduplication:

```
Schedule Due → Acquire Lock → Execute Flow → Release Lock
```

If lock acquisition fails (another instance already running), the execution is
skipped with status `skipped`.

---

## Run Status

Each scheduled execution records a status:

| Status    | Description                                      |
|-----------|--------------------------------------------------|
| `success` | Flow completed successfully                      |
| `failure` | Flow execution failed                            |
| `timeout` | Execution exceeded timeout                       |
| `skipped` | Lock not acquired (another instance ran it)      |

---

## Admin Endpoints

### List Schedules

```sh
curl http://localhost:8080/admin/schedules
```

Response:

```json
{
  "schedules": [
    {
      "id": "daily-report",
      "name": "Daily Sales Report",
      "schedule": "0 8 * * *",
      "timezone": "Asia/Jakarta",
      "flowId": "generate-report",
      "enabled": true,
      "nextRun": "2024-01-16T08:00:00+07:00",
      "lastRun": "2024-01-15T08:00:00+07:00",
      "lastStatus": "success"
    }
  ]
}
```

### Create Schedule

```sh
curl -X POST http://localhost:8080/admin/schedules \
  -H "Content-Type: application/json" \
  -d '{
    "id": "hourly-sync",
    "name": "Hourly Data Sync",
    "schedule": "@hourly",
    "timezone": "UTC",
    "flowId": "sync-data",
    "input": { "source": "external-api" },
    "enabled": true,
    "env": "production"
  }'
```

### Get Schedule

```sh
curl http://localhost:8080/admin/schedules/daily-report
```

### Update Schedule

```sh
curl -X PUT http://localhost:8080/admin/schedules/daily-report \
  -H "Content-Type: application/json" \
  -d '{
    "schedule": "0 9 * * *",
    "enabled": true
  }'
```

### Delete Schedule

```sh
curl -X DELETE http://localhost:8080/admin/schedules/daily-report
```

### Get Run History

```sh
curl http://localhost:8080/admin/schedules/daily-report/runs
```

Response:

```json
{
  "runs": [
    {
      "id": "run-123",
      "scheduleId": "daily-report",
      "startedAt": "2024-01-15T08:00:00Z",
      "completedAt": "2024-01-15T08:00:45Z",
      "status": "success",
      "traceId": "trace-abc"
    },
    {
      "id": "run-122",
      "scheduleId": "daily-report",
      "startedAt": "2024-01-14T08:00:00Z",
      "completedAt": "2024-01-14T08:01:12Z",
      "status": "success",
      "traceId": "trace-def"
    }
  ]
}
```

---

## Complete Examples

### Daily Report Generation

```json
{
  "id": "daily-sales-report",
  "name": "Daily Sales Report",
  "schedule": "0 8 * * *",
  "timezone": "Asia/Jakarta",
  "flowId": "generate-sales-report",
  "input": {
    "reportType": "daily",
    "format": "pdf",
    "recipients": ["sales@example.com", "manager@example.com"]
  },
  "enabled": true,
  "env": "production"
}
```

### Hourly Data Sync

```json
{
  "id": "hourly-inventory-sync",
  "name": "Hourly Inventory Sync",
  "schedule": "@hourly",
  "timezone": "UTC",
  "flowId": "sync-inventory",
  "input": {
    "source": "warehouse-api",
    "batchSize": 1000
  },
  "enabled": true,
  "env": "production"
}
```

### Weekly Cleanup

```json
{
  "id": "weekly-cleanup",
  "name": "Weekly Data Cleanup",
  "schedule": "0 2 * * 0",
  "timezone": "UTC",
  "flowId": "cleanup-old-data",
  "input": {
    "retentionDays": 90,
    "tables": ["logs", "sessions", "temp_data"]
  },
  "enabled": true,
  "env": "production"
}
```

### Every 5 Minutes Health Check

```json
{
  "id": "health-check",
  "name": "External API Health Check",
  "schedule": "@every 5m",
  "timezone": "UTC",
  "flowId": "check-api-health",
  "input": {
    "endpoints": [
      "https://api.payment.example.com/health",
      "https://api.shipping.example.com/health"
    ]
  },
  "enabled": true,
  "env": "production"
}
```

---

## Triggered Flow Example

Flow that receives scheduled input:

```json
{
  "id": "generate-sales-report",
  "tree": {
    "id": "trigger",
    "type": "trigger",
    "spec": {
      "method": "POST",
      "path": "/internal/generate-report",
      "input": { "body": true }
    },
    "children": [
      {
        "id": "seq-main",
        "type": "sequence",
        "spec": { "stopOnError": true },
        "children": [
          {
            "id": "log-start",
            "type": "logger",
            "spec": {
              "label": "report-generation-started",
              "level": "info",
              "capture": ["input.reportType", "input.format"]
            }
          },
          {
            "id": "fetch-data",
            "type": "action",
            "spec": {
              "connection": "pg-main",
              "operation": {
                "kind": "query",
                "payload": {
                  "sql": "SELECT * FROM sales WHERE date >= CURRENT_DATE - INTERVAL '1 day'"
                }
              },
              "saveAs": "salesData",
              "resilience": {
                "timeoutMs": 30000
              }
            }
          },
          {
            "id": "generate-report",
            "type": "action",
            "spec": {
              "connection": "report-api",
              "operation": {
                "kind": "http",
                "payload": {
                  "method": "POST",
                  "path": "/v1/reports",
                  "body": {
                    "type": "{{input.reportType}}",
                    "format": "{{input.format}}",
                    "data": "{{salesData}}"
                  }
                }
              },
              "saveAs": "report"
            }
          },
          {
            "id": "send-email",
            "type": "action",
            "spec": {
              "connection": "email-api",
              "operation": {
                "kind": "http",
                "payload": {
                  "method": "POST",
                  "path": "/v1/send",
                  "body": {
                    "to": "{{input.recipients}}",
                    "subject": "Daily Sales Report",
                    "attachmentUrl": "{{report.url}}"
                  }
                }
              },
              "onError": "continue"
            }
          }
        ]
      }
    ]
  }
}
```

---

## Best Practices

1. **Use appropriate timezones** — Set timezone explicitly for time-sensitive schedules.

2. **Avoid tight intervals** — Minimum is 1 minute; use reasonable intervals to avoid overload.

3. **Handle failures gracefully** — Design flows to handle partial failures and retries.

4. **Monitor run history** — Check `/admin/schedules/{id}/runs` for execution status.

5. **Use descriptive names** — Make schedule names clear for operational visibility.

6. **Disable before deleting** — Disable schedules first to ensure no in-flight executions.

7. **Test with dry-run** — Validate triggered flows using dry-run before enabling schedules.
