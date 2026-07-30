-- Stable same-Session branches and idempotent conservative Run retry.

CREATE TABLE session_branches (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    parent_branch_id  TEXT REFERENCES session_branches(id) ON DELETE SET NULL,
    fork_message_id   TEXT,
    leaf_message_id   TEXT,
    label             TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    FOREIGN KEY(session_id, fork_message_id)
        REFERENCES messages(session_id, id),
    FOREIGN KEY(session_id, leaf_message_id)
        REFERENCES messages(session_id, id)
);

CREATE INDEX ix_session_branches_session
    ON session_branches(session_id, created_at, id);
CREATE INDEX ix_session_branches_parent
    ON session_branches(parent_branch_id);

ALTER TABLE sessions ADD COLUMN active_branch_id TEXT REFERENCES session_branches(id);

INSERT INTO session_branches
    (id, session_id, parent_branch_id, fork_message_id, leaf_message_id, label, created_at, updated_at)
SELECT lower(hex(randomblob(16))), id, NULL, NULL, active_leaf_message_id, 'Main', created_at, updated_at
FROM sessions;

UPDATE sessions
SET active_branch_id = (
    SELECT b.id FROM session_branches b
    WHERE b.session_id = sessions.id AND b.parent_branch_id IS NULL
    ORDER BY b.created_at, b.id LIMIT 1
);

ALTER TABLE agent_runs ADD COLUMN retry_of_run_id TEXT REFERENCES agent_runs(id);
ALTER TABLE agent_runs ADD COLUMN retry_client_request_id TEXT;

CREATE UNIQUE INDEX ux_agent_runs_retry_request
    ON agent_runs(session_id, retry_client_request_id)
    WHERE retry_client_request_id IS NOT NULL;
CREATE INDEX ix_agent_runs_retry_source
    ON agent_runs(retry_of_run_id);
