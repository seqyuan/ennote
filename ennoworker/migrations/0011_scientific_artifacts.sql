ALTER TABLE artifacts ADD COLUMN source_tool_call_id TEXT NOT NULL DEFAULT '';
ALTER TABLE artifacts ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'upload';
ALTER TABLE artifacts ADD COLUMN source_workspace_path TEXT NOT NULL DEFAULT '';
ALTER TABLE artifacts ADD COLUMN retention_class TEXT NOT NULL DEFAULT 'project';

ALTER TABLE tool_calls ADD COLUMN raw_artifact_refs_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tool_calls ADD COLUMN projected_artifact_refs_json TEXT NOT NULL DEFAULT '[]';

CREATE INDEX IF NOT EXISTS idx_artifacts_run_tool
    ON artifacts(run_id, source_tool_call_id, created_at);
CREATE INDEX IF NOT EXISTS idx_artifacts_session_created
    ON artifacts(session_id, created_at, id);
