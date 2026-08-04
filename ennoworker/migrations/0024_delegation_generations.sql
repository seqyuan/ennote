-- Item 6 Stage 1: immutable delegation generations and attempts.
-- delegation_items stay stable logical assignments; generations carry explicit
-- retry selection and frozen authorization/budget snapshots; attempts are the
-- append-only execution records of one child Run. Generation 0 compatibility
-- columns on delegation_items are never rewritten by later generations.

-- 1. Group generation cursor.
ALTER TABLE delegation_groups ADD COLUMN current_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE delegation_groups ADD COLUMN updated_at TEXT;
ALTER TABLE delegation_groups ADD COLUMN completed_at TEXT;

-- 2. Group generations: one row per explicit generation (initial, retry,
--    input, follow_up). Selection is explicit; folding must never infer the
--    selected attempt by latest timestamp.
CREATE TABLE delegation_group_generations (
    id                            TEXT PRIMARY KEY,
    group_id                      TEXT NOT NULL REFERENCES delegation_groups(id),
    generation                    INTEGER NOT NULL,
    kind                          TEXT NOT NULL CHECK(kind IN ('initial','retry','input','follow_up')),
    status                        TEXT NOT NULL CHECK(status IN (
                                      'awaiting_authorization','queued','running','settled','failed','cancelled')),
    retry_selection_json          TEXT NOT NULL DEFAULT '[]',
    reused_attempts_json          TEXT NOT NULL DEFAULT '[]',
    authorization_snapshot_json   TEXT NOT NULL,
    authorization_snapshot_digest TEXT NOT NULL,
    budget_snapshot_json          TEXT NOT NULL,
    budget_snapshot_digest        TEXT NOT NULL,
    client_request_id             TEXT NOT NULL,
    created_at                    TEXT NOT NULL,
    completed_at                  TEXT,
    UNIQUE(group_id, generation),
    UNIQUE(group_id, client_request_id)
);

-- 3. Item attempts: append-only execution records. One attempt per child Run.
CREATE TABLE delegation_item_attempts (
    id                            TEXT PRIMARY KEY,
    item_id                       TEXT NOT NULL REFERENCES delegation_items(id),
    generation                    INTEGER NOT NULL,
    retry_of_attempt_id           TEXT REFERENCES delegation_item_attempts(id),
    child_run_id                  TEXT NOT NULL UNIQUE REFERENCES agent_runs(id),
    authorization_snapshot_json   TEXT NOT NULL,
    authorization_snapshot_digest TEXT NOT NULL,
    reserved_budget_json          TEXT NOT NULL,
    actual_usage_json             TEXT NOT NULL DEFAULT '{}',
    status                        TEXT NOT NULL CHECK(status IN (
                                      'queued','running','succeeded','blocked','needs_input',
                                      'not_authorized','failed','cancelled','interrupted')),
    terminal_kind                 TEXT,
    result_json                   TEXT,
    result_digest                 TEXT,
    error_code                    TEXT,
    error_message                 TEXT,
    root_reconciled_at            TEXT,
    created_at                    TEXT NOT NULL,
    started_at                    TEXT,
    finished_at                   TEXT,
    UNIQUE(item_id, generation)
);

-- 4. Durable retry-budget authorizations. The original parent may already be
--    terminal when a retry budget increase is decided, so this is independent
--    from tool-approval checkpoints.
CREATE TABLE delegation_approval_requests (
    id                         TEXT PRIMARY KEY,
    group_id                   TEXT NOT NULL REFERENCES delegation_groups(id),
    generation                 INTEGER NOT NULL,
    kind                       TEXT NOT NULL CHECK(kind IN ('retry_budget')),
    parent_run_id              TEXT NOT NULL REFERENCES agent_runs(id),
    session_id                 TEXT NOT NULL REFERENCES sessions(id),
    status                     TEXT NOT NULL CHECK(status IN ('pending','approved','rejected','cancelled')),
    items_json                 TEXT NOT NULL,
    decision_client_request_id TEXT,
    requested_at               TEXT NOT NULL,
    resolved_at                TEXT,
    UNIQUE(group_id, generation, kind)
);

CREATE INDEX ix_delegation_generations_group ON delegation_group_generations(group_id, generation);
CREATE INDEX ix_delegation_attempts_item ON delegation_item_attempts(item_id, generation);
CREATE INDEX ix_delegation_attempts_child ON delegation_item_attempts(child_run_id);
CREATE INDEX ix_delegation_approvals_pending ON delegation_approval_requests(status, session_id);

-- 5. Immutability triggers. Identity, snapshot, and budget columns never
--    change; a terminal attempt never returns to an active state.
CREATE TRIGGER delegation_attempts_immutable_identity
BEFORE UPDATE OF id,item_id,generation,retry_of_attempt_id,child_run_id,
    authorization_snapshot_json,authorization_snapshot_digest,reserved_budget_json
ON delegation_item_attempts
BEGIN
    SELECT RAISE(ABORT, 'attempt identity and snapshots are immutable');
END;

CREATE TRIGGER delegation_attempts_terminal_frozen
BEFORE UPDATE ON delegation_item_attempts
WHEN OLD.status IN ('succeeded','blocked','needs_input','not_authorized','failed','cancelled','interrupted')
     AND NEW.status <> OLD.status
BEGIN
    SELECT RAISE(ABORT, 'terminal attempts are frozen');
END;
