-- 000006_monitors: scheduled request monitors and their run history.
--
-- A monitor points at a synced `request` entity in the same workspace. The
-- worker claims due rows (FOR UPDATE SKIP LOCKED), advances next_run_at before
-- executing, and appends one monitor_runs row per attempt.

CREATE TABLE IF NOT EXISTS monitors (
    id            UUID        NOT NULL,
    workspace_id  TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          TEXT        NOT NULL,
    request_id    TEXT        NOT NULL,
    cron          TEXT        NOT NULL,
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    alert_webhook TEXT        NULL,
    next_run_at   TIMESTAMPTZ NOT NULL,
    last_run_at   TIMESTAMPTZ NULL,
    created_by    UUID        NOT NULL REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    CONSTRAINT monitors_workspace_name_key UNIQUE (workspace_id, name)
);

-- Worker claim: enabled rows whose deadline has passed, oldest first. The
-- partial index keeps the hot path small regardless of disabled monitors.
CREATE INDEX IF NOT EXISTS idx_monitors_claim ON monitors (enabled, next_run_at);
CREATE INDEX IF NOT EXISTS idx_monitors_workspace ON monitors (workspace_id, created_at);

CREATE TABLE IF NOT EXISTS monitor_runs (
    id                 UUID        NOT NULL,
    monitor_id         UUID        NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    started_at         TIMESTAMPTZ NOT NULL,
    finished_at        TIMESTAMPTZ NULL,
    ok                 BOOLEAN     NOT NULL DEFAULT FALSE,
    status_code        INT         NOT NULL DEFAULT 0,
    duration_ms        INT         NOT NULL DEFAULT 0,
    assertion_failures INT         NOT NULL DEFAULT 0,
    error              TEXT        NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id)
);

-- Run history is always read per monitor, newest first.
CREATE INDEX IF NOT EXISTS idx_monitor_runs_monitor_started
    ON monitor_runs (monitor_id, started_at DESC);
-- Transition checks ("was the previous run ok?") and fleet-wide failure scans.
CREATE INDEX IF NOT EXISTS idx_monitor_runs_ok_started
    ON monitor_runs (ok, started_at);
