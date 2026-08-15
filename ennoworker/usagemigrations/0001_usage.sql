CREATE TABLE IF NOT EXISTS usage_aggregates (
    usage_day                    TEXT NOT NULL,
    project_id                   TEXT NOT NULL,
    session_id                   TEXT NOT NULL,
    provider_id                  TEXT NOT NULL,
    model_id                     TEXT NOT NULL,
    input_tokens                 INTEGER NOT NULL DEFAULT 0,
    output_tokens                INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens            INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens             INTEGER NOT NULL DEFAULT 0,
    cost_usd_micros              INTEGER NOT NULL DEFAULT 0,
    updated_at                   TEXT NOT NULL,
    PRIMARY KEY(usage_day, project_id, session_id, provider_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_aggregates_project_day
ON usage_aggregates(project_id, usage_day DESC);

CREATE TABLE IF NOT EXISTS applied_projection_events (
    event_id         TEXT PRIMARY KEY,
    session_id       TEXT NOT NULL,
    event_type       TEXT NOT NULL,
    applied_at       TEXT NOT NULL
);
