-- Durable, branch-aware context compaction.

CREATE TABLE policy_profiles_v7 (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('tool', 'turn', 'vision', 'compaction')),
    version     INTEGER NOT NULL DEFAULT 1,
    config_json TEXT NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(kind, name, version)
);

INSERT INTO policy_profiles_v7
    (id, name, kind, version, config_json, status, created_at, updated_at)
SELECT id, name, kind, version, config_json, status, created_at, updated_at
FROM policy_profiles;

DROP TABLE policy_profiles;
ALTER TABLE policy_profiles_v7 RENAME TO policy_profiles;
CREATE INDEX ix_policy_profiles_kind_status
    ON policy_profiles(kind, status, name, version);

INSERT INTO policy_profiles
    (id, name, kind, version, config_json, status, created_at, updated_at)
VALUES
    ('builtin-compaction-manual-only-v1', 'manual_only', 'compaction', 1,
     '{"mode":"manual_only","triggerRatio":0.75,"keepRecentTurns":2,"tailTokenRatio":0.20,"tailMinTokens":8000,"tailMaxTokens":32000,"summaryInputRatio":0.70,"compactionModelProfileId":null,"summaryMaxOutputTokens":4096,"includeReasoning":false,"allowHistoryLookup":true,"allowOverflowRecovery":true,"maxOverflowRecoveries":1,"ineffectiveReclaimRatio":0.10,"ineffectiveLimit":3,"failureCooldownSeconds":600,"promptVersion":"v1"}',
     'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

INSERT INTO settings(key, value) VALUES
    ('default_compaction_policy_profile_id', 'builtin-compaction-manual-only-v1');

ALTER TABLE agent_profiles ADD COLUMN compaction_policy_profile_id TEXT REFERENCES policy_profiles(id);
ALTER TABLE sessions ADD COLUMN compaction_policy_profile_id TEXT REFERENCES policy_profiles(id);

CREATE TABLE agent_runs_v7 (
    id                         TEXT PRIMARY KEY,
    turn_id                    TEXT REFERENCES turns(id) ON DELETE CASCADE,
    session_id                 TEXT NOT NULL REFERENCES sessions(id),
    run_kind                   TEXT NOT NULL DEFAULT 'agent'
                                     CHECK(run_kind IN ('agent', 'context_compaction')),
    base_message_id            TEXT REFERENCES messages(id),
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
    created_at                 TEXT NOT NULL,
    CHECK((run_kind = 'agent' AND turn_id IS NOT NULL) OR
          (run_kind = 'context_compaction' AND turn_id IS NULL AND base_message_id IS NOT NULL))
);

INSERT INTO agent_runs_v7
    (id, turn_id, session_id, run_kind, base_message_id, attempt, status,
     assistant_message_id, requested_config_json, effective_config_json,
     system_prompt_digest, tool_policy_digest, skill_snapshot_digest,
     error_code, error_message, started_at, finished_at, heartbeat_at, created_at)
SELECT id, turn_id, session_id, 'agent', NULL, attempt, status,
       assistant_message_id, requested_config_json, effective_config_json,
       system_prompt_digest, tool_policy_digest, skill_snapshot_digest,
       error_code, error_message, started_at, finished_at, heartbeat_at, created_at
FROM agent_runs;

DROP TABLE agent_runs;
ALTER TABLE agent_runs_v7 RENAME TO agent_runs;
CREATE INDEX ix_agent_runs_session ON agent_runs(session_id, created_at DESC);
CREATE INDEX ix_agent_runs_turn ON agent_runs(turn_id);
CREATE UNIQUE INDEX ux_agent_runs_one_active
    ON agent_runs(session_id) WHERE status IN ('queued', 'running');

CREATE TABLE context_compactions (
    id                         TEXT PRIMARY KEY,
    run_id                     TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,
    session_id                 TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    client_request_id          TEXT,
    status                     TEXT NOT NULL,
    reason                     TEXT NOT NULL,
    policy_profile_id          TEXT,
    policy_version             INTEGER,
    requested_config_json      TEXT NOT NULL,
    effective_config_json      TEXT NOT NULL,
    base_leaf_message_id       TEXT NOT NULL,
    previous_compaction_id     TEXT REFERENCES context_compactions(id) ON DELETE SET NULL,
    source_from_message_id     TEXT,
    source_through_message_id  TEXT,
    first_kept_message_id      TEXT NOT NULL,
    source_digest              TEXT NOT NULL,
    summary_contract_digest    TEXT NOT NULL,
    summary                    TEXT NOT NULL DEFAULT '',
    summary_digest             TEXT NOT NULL DEFAULT '',
    prompt_version             TEXT NOT NULL,
    custom_instructions        TEXT NOT NULL DEFAULT '',
    model_call_id              TEXT REFERENCES model_calls(id) ON DELETE SET NULL,
    tokens_before              INTEGER NOT NULL DEFAULT 0,
    estimated_tokens_after     INTEGER NOT NULL DEFAULT 0,
    reclaimed_tokens           INTEGER NOT NULL DEFAULT 0,
    error_code                 TEXT,
    error_message              TEXT,
    started_at                 TEXT,
    finished_at                TEXT,
    created_at                 TEXT NOT NULL,
    CHECK(status IN ('planned','running','completed','failed','cancelled')),
    CHECK(reason IN ('manual','threshold','overflow')),
    CHECK(status <> 'completed' OR (
        source_through_message_id IS NOT NULL AND summary <> '' AND
        summary_digest <> '' AND finished_at IS NOT NULL
    )),
    UNIQUE(session_id, client_request_id)
);

CREATE UNIQUE INDEX ux_context_compactions_one_active
    ON context_compactions(session_id) WHERE status IN ('planned', 'running');
CREATE INDEX ix_context_compactions_session_completed
    ON context_compactions(session_id, status, created_at DESC);
CREATE INDEX ix_context_compactions_reuse
    ON context_compactions(session_id, source_digest, summary_contract_digest, status);
CREATE INDEX ix_context_compactions_previous
    ON context_compactions(previous_compaction_id);
CREATE INDEX ix_context_compactions_base_leaf
    ON context_compactions(session_id, base_leaf_message_id, status);

CREATE TABLE session_compaction_state (
    session_id                    TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    failure_cooldown_until        TEXT,
    last_failure_code             TEXT,
    ineffective_count             INTEGER NOT NULL DEFAULT 0,
    last_reclaim_ratio            REAL,
    updated_at                    TEXT NOT NULL
);

ALTER TABLE model_calls ADD COLUMN request_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_calls ADD COLUMN compaction_id TEXT REFERENCES context_compactions(id) ON DELETE SET NULL;

DROP INDEX IF EXISTS ux_model_calls_run_iteration_purpose_attempt_source;
CREATE UNIQUE INDEX ux_model_calls_run_generation_iteration_purpose_attempt_source
    ON model_calls(run_id, request_generation, iteration, purpose, attempt, source_artifact_id);
