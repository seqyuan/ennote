CREATE TABLE IF NOT EXISTS project_summaries (
    project_id       TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL,
    workspace_path   TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS session_summaries (
    session_id       TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL,
    title            TEXT NOT NULL,
    status           TEXT NOT NULL,
    active_run_id    TEXT,
    requires_action  INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_summaries_project
ON session_summaries(project_id, status, updated_at DESC, session_id);

CREATE TABLE IF NOT EXISTS owner_index (
    resource_kind    TEXT NOT NULL,
    resource_id      TEXT NOT NULL,
    project_id       TEXT NOT NULL,
    session_id       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    PRIMARY KEY(resource_kind, resource_id)
);

CREATE INDEX IF NOT EXISTS idx_owner_index_session
ON owner_index(session_id, resource_kind);

CREATE TABLE IF NOT EXISTS attention_summaries (
    attention_id     TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL,
    session_id       TEXT NOT NULL,
    kind             TEXT NOT NULL,
    status           TEXT NOT NULL,
    requires_action  INTEGER NOT NULL,
    display_json     TEXT NOT NULL DEFAULT '{}',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_attention_summaries_open
ON attention_summaries(status, requires_action, updated_at DESC);

CREATE TABLE IF NOT EXISTS resource_diagnostics (
    resource_kind    TEXT NOT NULL,
    resource_id      TEXT NOT NULL,
    digest           TEXT NOT NULL DEFAULT '',
    error            TEXT NOT NULL DEFAULT '',
    indexed_at       TEXT NOT NULL,
    PRIMARY KEY(resource_kind, resource_id)
);

CREATE TABLE IF NOT EXISTS applied_projection_events (
    event_id         TEXT PRIMARY KEY,
    session_id       TEXT NOT NULL,
    event_type       TEXT NOT NULL,
    applied_at       TEXT NOT NULL
);
