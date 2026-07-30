CREATE INDEX IF NOT EXISTS idx_sessions_project_status_updated
    ON sessions(project_id, status, updated_at DESC, id);
