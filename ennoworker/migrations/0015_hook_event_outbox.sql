-- Durable outbox for observer hooks (RunEnd, ApprovalRequested, Notification).
-- One row per durable event; the background worker fans out to matching hooks
-- at delivery time using the run's frozen hook set.

CREATE TABLE IF NOT EXISTS hook_event_outbox (
    delivery_id     TEXT PRIMARY KEY,
    event_id        INTEGER NOT NULL UNIQUE,
    run_id          TEXT NOT NULL,
    session_id      TEXT NOT NULL DEFAULT '',
    event_type      TEXT NOT NULL,
    payload_json    TEXT NOT NULL DEFAULT '{}',
    workspace_id    TEXT NOT NULL DEFAULT '',
    workspace_root  TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL CHECK(status IN ('pending','delivering','delivered','dead')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    last_error      TEXT,
    created_at      TEXT NOT NULL,
    delivered_at    TEXT
);
