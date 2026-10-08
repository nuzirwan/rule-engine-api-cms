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
