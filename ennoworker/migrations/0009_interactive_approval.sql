-- Durable, batch-scoped Interactive Approval.

INSERT INTO policy_profiles
    (id, name, kind, version, config_json, status, created_at, updated_at)
VALUES
    ('builtin-tool-ask-v1', 'Ask', 'tool', 1,
     '{"mode":"ask","allowedTools":[],"allowedExecutables":["git","rg","ls","cat","sed","find","head","tail","wc","pwd","mkdir","cp","mv","touch","npm","npx","node","go","gofmt","make"],"deniedSubcommands":{"git":["push"],"npm":["publish"]},"allowPipes":true,"allowCommandSubstitution":false,"allowedWriteRoots":["/workspace"],"maxTimeoutSeconds":300,"redactPatterns":["(?i)(api[_-]?key|token|secret|password)\\s*[:=]\\s*[^\\s]+"]}',
     'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

DROP INDEX ux_agent_runs_one_active;
CREATE UNIQUE INDEX ux_agent_runs_one_active
    ON agent_runs(session_id)
    WHERE status IN ('queued', 'running', 'waiting_for_approval');

CREATE TABLE run_execution_checkpoints (
    id             TEXT PRIMARY KEY,
    run_id         TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL,
    iteration      INTEGER NOT NULL,
    batch_digest   TEXT NOT NULL,
    state_json     TEXT NOT NULL,
    status         TEXT NOT NULL CHECK(status IN ('pending','executing','consumed','cancelled','interrupted')),
    created_at     TEXT NOT NULL,
    started_at     TEXT,
    finished_at    TEXT
);

CREATE INDEX ix_execution_checkpoints_run
    ON run_execution_checkpoints(run_id, created_at DESC);
CREATE UNIQUE INDEX ux_execution_checkpoint_pending
    ON run_execution_checkpoints(run_id)
    WHERE status IN ('pending', 'executing');

CREATE TABLE tool_approval_requests (
    id                         TEXT PRIMARY KEY,
    run_id                     TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    session_id                 TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    checkpoint_id              TEXT NOT NULL UNIQUE REFERENCES run_execution_checkpoints(id) ON DELETE CASCADE,
    iteration                  INTEGER NOT NULL,
    batch_digest               TEXT NOT NULL,
    status                     TEXT NOT NULL CHECK(status IN ('pending','approved','rejected','cancelled')),
    items_json                 TEXT NOT NULL,
    decision_client_request_id TEXT,
    requested_at               TEXT NOT NULL,
    resolved_at                TEXT
);

CREATE INDEX ix_tool_approvals_session
    ON tool_approval_requests(session_id, requested_at DESC);
CREATE INDEX ix_tool_approvals_run
    ON tool_approval_requests(run_id, requested_at DESC);
CREATE UNIQUE INDEX ux_tool_approval_pending
    ON tool_approval_requests(run_id)
    WHERE status = 'pending';
