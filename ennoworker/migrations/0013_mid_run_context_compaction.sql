-- Durable audit records for context compaction inside an active Agent Run.

CREATE TABLE run_context_compactions (
    id                         TEXT PRIMARY KEY,
    run_id                     TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    previous_compaction_id     TEXT REFERENCES run_context_compactions(id) ON DELETE SET NULL,
    status                     TEXT NOT NULL CHECK(status IN ('planned','running','completed','failed','cancelled')),
    reason                     TEXT NOT NULL CHECK(reason IN ('threshold','overflow')),
    iteration                  INTEGER NOT NULL CHECK(iteration > 1),
    request_generation         INTEGER NOT NULL CHECK(request_generation >= 0),
    policy_profile_id          TEXT,
    policy_version             INTEGER,
    effective_config_json      TEXT NOT NULL DEFAULT '{}',
    source_digest              TEXT NOT NULL,
    summary_contract_digest    TEXT NOT NULL,
    summary                    TEXT NOT NULL DEFAULT '',
    summary_digest             TEXT NOT NULL DEFAULT '',
    covered_generated          INTEGER NOT NULL DEFAULT 0 CHECK(covered_generated >= 0),
    model_call_id              TEXT REFERENCES model_calls(id) ON DELETE SET NULL,
    tokens_before              INTEGER NOT NULL DEFAULT 0,
    estimated_tokens_after     INTEGER NOT NULL DEFAULT 0,
    reclaimed_tokens           INTEGER NOT NULL DEFAULT 0,
    error_code                 TEXT,
    error_message              TEXT,
    started_at                 TEXT,
    finished_at                TEXT,
    created_at                 TEXT NOT NULL,
    CHECK(status <> 'completed' OR (
        summary <> '' AND summary_digest <> '' AND model_call_id IS NOT NULL AND finished_at IS NOT NULL
    ))
);

CREATE UNIQUE INDEX ux_run_context_compactions_one_active
    ON run_context_compactions(run_id) WHERE status IN ('planned','running');
CREATE UNIQUE INDEX ux_run_context_compactions_reuse
    ON run_context_compactions(run_id,source_digest,summary_contract_digest)
    WHERE status='completed';
CREATE INDEX ix_run_context_compactions_run
    ON run_context_compactions(run_id,created_at,id);
CREATE INDEX ix_run_context_compactions_previous
    ON run_context_compactions(previous_compaction_id);
