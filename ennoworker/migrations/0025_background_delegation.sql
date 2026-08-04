-- Item 6 Stage 2: durable background delegation.
-- Background children outlive their parent Run. One stable handle per group,
-- exactly one logical completion per handle/generation, replay-safe delivery
-- events, and an optional ordered auto-resume continuation Run. All of these
-- are projections of terminal Run/attempt facts — never a second execution or
-- authorization state machine.

-- 1. Group handles: stable identity for delivery. Blocking groups get one
--    handle too, so every delegation is addressable uniformly.
CREATE TABLE delegation_handles (
    id                    TEXT PRIMARY KEY,
    group_id              TEXT NOT NULL UNIQUE REFERENCES delegation_groups(id),
    session_id            TEXT NOT NULL REFERENCES sessions(id),
    source_parent_run_id  TEXT NOT NULL REFERENCES agent_runs(id),
    source_branch_id      TEXT NOT NULL REFERENCES session_branches(id),
    execution_mode        TEXT NOT NULL CHECK(execution_mode IN ('blocking','background')),
    auto_resume           INTEGER NOT NULL DEFAULT 0 CHECK(auto_resume IN (0,1)),
    status                TEXT NOT NULL CHECK(status IN ('active','completed','cancelled')),
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

-- 2. Per-session monotonic delivery sequence (CAS-allocated).
CREATE TABLE session_delivery_sequences (
    session_id    TEXT PRIMARY KEY REFERENCES sessions(id),
    next_sequence INTEGER NOT NULL DEFAULT 1
);

-- 3. Logical completions: exactly one per handle/generation. The sequence is
--    the session-wide delivery order used by optional auto-resume.
CREATE TABLE delegation_completions (
    id                    TEXT PRIMARY KEY,
    handle_id             TEXT NOT NULL REFERENCES delegation_handles(id),
    session_id            TEXT NOT NULL REFERENCES sessions(id),
    generation            INTEGER NOT NULL,
    kind                  TEXT NOT NULL CHECK(kind IN ('completed','partial','failed','cancelled')),
    result_json           TEXT NOT NULL,
    result_digest         TEXT NOT NULL,
    sequence              INTEGER NOT NULL,
    delivery_status       TEXT NOT NULL CHECK(delivery_status IN (
                              'pending','consumed_by_parent','notified','resume_queued','resume_completed')),
    resume_run_id         TEXT REFERENCES agent_runs(id),
    created_at            TEXT NOT NULL,
    delivered_at          TEXT,
    UNIQUE(handle_id, generation),
    UNIQUE(session_id, sequence)
);

-- 4. Durable delivery events for replay-safe SSE. The event id is the replay
--    cursor; clients dedupe by event id and source key.
CREATE TABLE delivery_events (
    event_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id        TEXT NOT NULL REFERENCES sessions(id),
    source_kind       TEXT NOT NULL,
    source_id         TEXT NOT NULL,
    source_generation INTEGER NOT NULL,
    event_type        TEXT NOT NULL,
    payload_json      TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    UNIQUE(source_kind, source_id, source_generation, event_type)
);
CREATE INDEX ix_delivery_events_session ON delivery_events(session_id, event_id);

-- 5. Continuation Runs: a top-level Host Run created by auto-resume. The
--    uniqueness constraint is enforced in Go (partial unique index) so a
--    source completion can drive at most one continuation.
ALTER TABLE agent_runs ADD COLUMN source_completion_id TEXT;
CREATE UNIQUE INDEX ux_agent_runs_source_completion
    ON agent_runs(source_completion_id)
    WHERE source_completion_id IS NOT NULL;
