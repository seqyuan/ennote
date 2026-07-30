-- Durable mid-run steering and follow-up queue.
CREATE TABLE IF NOT EXISTS run_input_queue (
    id                TEXT PRIMARY KEY,
    run_id            TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    client_request_id TEXT NOT NULL,
    seq               INTEGER NOT NULL,
    kind              TEXT NOT NULL CHECK(kind IN ('steer', 'follow_up')),
    content_json      TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'queued'
                      CHECK(status IN ('queued', 'injected', 'cancelled')),
    created_at        TEXT NOT NULL,
    injected_at       TEXT,
    cancelled_at      TEXT,
    UNIQUE(run_id, client_request_id),
    UNIQUE(run_id, seq)
);

CREATE INDEX IF NOT EXISTS ix_run_input_queue_pending
    ON run_input_queue(run_id, kind, status, seq);
