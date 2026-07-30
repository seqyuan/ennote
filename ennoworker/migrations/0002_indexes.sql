-- Ennote v0.1 indexes and constraints

-- Project / Workspace uniqueness
CREATE UNIQUE INDEX IF NOT EXISTS ux_project_workspace_active
    ON project_workspaces(project_id)
    WHERE status = 'active';

-- Session
CREATE INDEX IF NOT EXISTS ix_sessions_project ON sessions(project_id, updated_at DESC);

-- Messages
CREATE INDEX IF NOT EXISTS ix_messages_session ON messages(session_id, created_at);
CREATE INDEX IF NOT EXISTS ix_messages_parent ON messages(parent_message_id);

-- Message parts
CREATE INDEX IF NOT EXISTS ix_message_parts_message ON message_parts(message_id, ordinal);

-- Turns
CREATE UNIQUE INDEX IF NOT EXISTS ux_turns_client_request
    ON turns(session_id, client_request_id);

-- Agent runs
CREATE INDEX IF NOT EXISTS ix_agent_runs_session ON agent_runs(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_agent_runs_turn ON agent_runs(turn_id);

CREATE UNIQUE INDEX IF NOT EXISTS ux_agent_runs_one_active
    ON agent_runs(session_id)
    WHERE status IN ('queued', 'running');

-- Run events
CREATE INDEX IF NOT EXISTS ix_run_events_run ON run_events(run_id, seq);
CREATE UNIQUE INDEX IF NOT EXISTS ux_run_events_seq ON run_events(run_id, seq);

-- Model calls
CREATE INDEX IF NOT EXISTS ix_model_calls_run ON model_calls(run_id, seq);

-- Tool calls
CREATE INDEX IF NOT EXISTS ix_tool_calls_run ON tool_calls(run_id, seq);

-- Skill snapshots
CREATE INDEX IF NOT EXISTS ix_skill_snapshots_run ON skill_snapshots(run_id);

-- Artifacts
CREATE INDEX IF NOT EXISTS ix_artifacts_project ON artifacts(project_id);
CREATE INDEX IF NOT EXISTS ix_artifacts_session ON artifacts(session_id);
