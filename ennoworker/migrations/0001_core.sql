-- Ennote v0.1 core schema
-- (WAL and foreign_keys are set via connection string in store.Open)

CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    applied_at  TEXT NOT NULL
);

-- 4.1 Project and Workspace
CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS project_workspaces (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL DEFAULT 'local',
    host_path        TEXT NOT NULL,
    virtual_path     TEXT NOT NULL DEFAULT '/workspace',
    status           TEXT NOT NULL DEFAULT 'active',
    path_fingerprint TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL
);

-- 4.2 Provider, Model, and Agent Profile
CREATE TABLE IF NOT EXISTS provider_profiles (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    provider_type   TEXT NOT NULL,
    base_url        TEXT NOT NULL DEFAULT '',
    credential_ref  TEXT NOT NULL DEFAULT '',
    proxy           TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS model_profiles (
    id                TEXT PRIMARY KEY,
    provider_id       TEXT NOT NULL REFERENCES provider_profiles(id) ON DELETE CASCADE,
    model_name        TEXT NOT NULL,
    display_name      TEXT NOT NULL,
    context_window    INTEGER NOT NULL DEFAULT 131072,
    max_output_tokens INTEGER NOT NULL DEFAULT 16384,
    supports_vision   INTEGER NOT NULL DEFAULT 0,
    supports_tool_use INTEGER NOT NULL DEFAULT 1,
    supports_thinking INTEGER NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'active',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_profiles (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    system_prompt        TEXT NOT NULL DEFAULT '',
    tool_policy          TEXT NOT NULL DEFAULT 'default',
    default_model_id     TEXT REFERENCES model_profiles(id),
    status               TEXT NOT NULL DEFAULT 'active',
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

-- 4.3 Session and message tree
CREATE TABLE IF NOT EXISTS sessions (
    id                         TEXT PRIMARY KEY,
    project_id                 TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title                      TEXT NOT NULL DEFAULT 'New Session',
    status                     TEXT NOT NULL DEFAULT 'active',
    active_leaf_message_id     TEXT,
    default_agent_profile_id   TEXT REFERENCES agent_profiles(id),
    default_model_profile_id   TEXT REFERENCES model_profiles(id),
    source_session_id          TEXT,
    source_message_id          TEXT,
    created_at                 TEXT NOT NULL,
    updated_at                 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    parent_message_id TEXT,
    role              TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'complete',
    run_id            TEXT,
    created_at        TEXT NOT NULL,
    UNIQUE(session_id, id),
    FOREIGN KEY(session_id, parent_message_id)
        REFERENCES messages(session_id, id)
);

CREATE TABLE IF NOT EXISTS message_parts (
    id           TEXT PRIMARY KEY,
    message_id   TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    ordinal      INTEGER NOT NULL,
    block_kind   TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    UNIQUE(message_id, ordinal)
);

-- 4.4 Turn and Agent Run
CREATE TABLE IF NOT EXISTS turns (
    id                 TEXT PRIMARY KEY,
    session_id         TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    client_request_id  TEXT NOT NULL,
    user_message_id    TEXT NOT NULL REFERENCES messages(id),
    base_message_id    TEXT,
    status             TEXT NOT NULL DEFAULT 'pending',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_runs (
    id                         TEXT PRIMARY KEY,
    turn_id                    TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    session_id                 TEXT NOT NULL REFERENCES sessions(id),
    attempt                    INTEGER NOT NULL DEFAULT 1,
    status                     TEXT NOT NULL DEFAULT 'queued',
    assistant_message_id       TEXT,
    requested_config_json      TEXT NOT NULL DEFAULT '{}',
    effective_config_json      TEXT NOT NULL DEFAULT '{}',
    system_prompt_digest       TEXT NOT NULL DEFAULT '',
    tool_policy_digest         TEXT NOT NULL DEFAULT '',
    skill_snapshot_digest      TEXT NOT NULL DEFAULT '',
    error_code                 TEXT,
    error_message              TEXT,
    started_at                 TEXT,
    finished_at                TEXT,
    heartbeat_at               TEXT,
    created_at                 TEXT NOT NULL
);

-- 4.5 Append-only run events
CREATE TABLE IF NOT EXISTS run_events (
    event_id     INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    event_type   TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL
);

-- 4.6 Model call, tool call, usage
CREATE TABLE IF NOT EXISTS model_calls (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq                 INTEGER NOT NULL,
    provider_profile_id TEXT,
    model_profile_id    TEXT,
    actual_model        TEXT NOT NULL DEFAULT '',
    requested_config_json TEXT NOT NULL DEFAULT '{}',
    effective_config_json TEXT NOT NULL DEFAULT '{}',
    stop_reason         TEXT,
    http_status         INTEGER,
    error_code          TEXT,
    input_tokens        INTEGER NOT NULL DEFAULT 0,
    output_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens    INTEGER NOT NULL DEFAULT 0,
    started_at          TEXT NOT NULL,
    finished_at         TEXT
);

CREATE TABLE IF NOT EXISTS tool_calls (
    id                TEXT PRIMARY KEY,
    run_id            TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq               INTEGER NOT NULL,
    tool_call_id      TEXT NOT NULL,
    tool_name         TEXT NOT NULL,
    arguments_json    TEXT NOT NULL DEFAULT '{}',
    status            TEXT NOT NULL DEFAULT 'pending',
    result_preview    TEXT NOT NULL DEFAULT '',
    result_path       TEXT,
    is_error          INTEGER NOT NULL DEFAULT 0,
    exit_code         INTEGER,
    started_at        TEXT NOT NULL,
    finished_at       TEXT
);

CREATE TABLE IF NOT EXISTS usage_records (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    ref_id      TEXT NOT NULL,
    details_json TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL
);

-- 4.7 Skill snapshots, artifacts, settings
CREATE TABLE IF NOT EXISTS skill_snapshots (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    skill_id      TEXT NOT NULL,
    version       TEXT NOT NULL DEFAULT '',
    manifest_digest TEXT NOT NULL DEFAULT '',
    content_digest TEXT NOT NULL DEFAULT '',
    snapshot_path TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS artifacts (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id     TEXT,
    message_id     TEXT,
    run_id         TEXT,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL DEFAULT 'file',
    mime_type      TEXT NOT NULL DEFAULT 'application/octet-stream',
    storage_path   TEXT NOT NULL,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    sha256         TEXT NOT NULL DEFAULT '',
    metadata_json  TEXT NOT NULL DEFAULT '{}',
    created_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
