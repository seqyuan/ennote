-- The composed system prompt is stored once per distinct composition and
-- referenced by content digest from agent_runs.system_prompt_snapshot_json.
-- Runs that compose an identical prompt — the common case, since the same Role
-- and project instruction files repeat across turns — share one row, and run
-- rows stay small enough to list.
CREATE TABLE prompt_blobs (
    digest     TEXT PRIMARY KEY,
    bytes      INTEGER NOT NULL,
    text       TEXT NOT NULL,
    created_at TEXT NOT NULL
);
