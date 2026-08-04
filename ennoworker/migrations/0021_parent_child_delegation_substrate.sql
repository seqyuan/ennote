-- Durable parent/child delegation substrate.
-- Stage 3 infrastructure: delegation groups, items, run budgets, and the
-- system-owned Workspace Explorer builtin Role used to qualify the substrate.

-- 1. Delegation groups — one per parent tool call.
CREATE TABLE delegation_groups (
    id                  TEXT PRIMARY KEY,
    parent_run_id       TEXT NOT NULL REFERENCES agent_runs(id),
    parent_tool_call_id TEXT NOT NULL,
    strategy            TEXT NOT NULL DEFAULT 'single',
    status              TEXT NOT NULL DEFAULT 'pending',
    created_at          TEXT NOT NULL,
    UNIQUE(parent_run_id, parent_tool_call_id)
);

-- 2. Delegation items — one per child assignment.
CREATE TABLE delegation_items (
    id               TEXT PRIMARY KEY,
    group_id         TEXT NOT NULL REFERENCES delegation_groups(id),
    child_run_id     TEXT REFERENCES agent_runs(id),
    name             TEXT NOT NULL,
    role_version_id  TEXT NOT NULL REFERENCES agent_profile_versions(id),
    assignment_json  TEXT NOT NULL DEFAULT '{}',
    output_contract  TEXT NOT NULL DEFAULT 'text-v1',
    budget_json      TEXT NOT NULL DEFAULT '{}',
    result_json      TEXT,
    status           TEXT NOT NULL DEFAULT 'pending',
    ordinal          INTEGER NOT NULL,
    created_at       TEXT NOT NULL,
    UNIQUE(group_id, ordinal),
    UNIQUE(child_run_id)
);
CREATE INDEX ix_delegation_items_group ON delegation_items(group_id);

-- 3. Per-Run budget tracking with CAS guard.
CREATE TABLE run_budgets (
    run_id               TEXT PRIMARY KEY REFERENCES agent_runs(id),
    max_model_calls      INTEGER NOT NULL DEFAULT 0,
    max_tool_calls       INTEGER NOT NULL DEFAULT 0,
    max_total_tokens     INTEGER NOT NULL DEFAULT 0,
    max_output_tokens    INTEGER NOT NULL DEFAULT 0,
    max_cost_usd_micros  INTEGER NOT NULL DEFAULT 0,
    max_wall_time_ms     INTEGER NOT NULL DEFAULT 0,
    consumed_model_calls INTEGER NOT NULL DEFAULT 0,
    consumed_tool_calls  INTEGER NOT NULL DEFAULT 0,
    consumed_tokens      INTEGER NOT NULL DEFAULT 0,
    reserved_at          TEXT NOT NULL
);

-- 4. Rebuild agent_runs to allow the delegated_agent Run kind and the
--    child-aware active Run constraint. The run_kind CHECK is baked into the
--    table, so SQLite requires a full table rebuild. Migration 21 runs with
--    foreign_keys OFF (see store.Migrate) and a foreign_key_check afterwards.
CREATE TABLE agent_runs_v21 (
    id                           TEXT PRIMARY KEY,
    turn_id                      TEXT REFERENCES turns(id) ON DELETE CASCADE,
    session_id                   TEXT NOT NULL REFERENCES sessions(id),
    run_kind                     TEXT NOT NULL DEFAULT 'agent'
                                     CHECK(run_kind IN ('agent','context_compaction','delegated_agent')),
    base_message_id              TEXT REFERENCES messages(id),
    attempt                      INTEGER NOT NULL DEFAULT 1,
    status                       TEXT NOT NULL DEFAULT 'queued',
    assistant_message_id         TEXT,
    requested_config_json        TEXT NOT NULL DEFAULT '{}',
    effective_config_json        TEXT NOT NULL DEFAULT '{}',
    system_prompt_digest         TEXT NOT NULL DEFAULT '',
    tool_policy_digest           TEXT NOT NULL DEFAULT '',
    skill_snapshot_digest        TEXT NOT NULL DEFAULT '',
    error_code                   TEXT,
    error_message                TEXT,
    started_at                   TEXT,
    finished_at                  TEXT,
    heartbeat_at                 TEXT,
    created_at                   TEXT NOT NULL,
    retry_of_run_id              TEXT REFERENCES agent_runs(id),
    retry_client_request_id      TEXT,
    speaker_snapshot_json        TEXT NOT NULL DEFAULT '{}',
    context_snapshot_json        TEXT NOT NULL DEFAULT '{}',
    context_snapshot_digest      TEXT NOT NULL DEFAULT '',
    parent_run_id                TEXT REFERENCES agent_runs(id),
    root_run_id                  TEXT REFERENCES agent_runs(id),
    execution_depth              INTEGER NOT NULL DEFAULT 0,
    publish_mode                 TEXT NOT NULL DEFAULT 'public_final',
    commit_format_version        INTEGER NOT NULL DEFAULT 1,
    system_prompt_snapshot_json  TEXT NOT NULL DEFAULT '{}',
    CHECK((run_kind = 'agent' AND turn_id IS NOT NULL) OR
          (run_kind = 'context_compaction' AND turn_id IS NULL AND base_message_id IS NOT NULL) OR
          (run_kind = 'delegated_agent' AND parent_run_id IS NOT NULL))
);

INSERT INTO agent_runs_v21
    (id, turn_id, session_id, run_kind, base_message_id, attempt, status,
     assistant_message_id, requested_config_json, effective_config_json,
     system_prompt_digest, tool_policy_digest, skill_snapshot_digest,
     error_code, error_message, started_at, finished_at, heartbeat_at, created_at,
     retry_of_run_id, retry_client_request_id, speaker_snapshot_json,
     context_snapshot_json, context_snapshot_digest, parent_run_id, root_run_id,
     execution_depth, publish_mode, commit_format_version, system_prompt_snapshot_json)
SELECT id, turn_id, session_id, run_kind, base_message_id, attempt, status,
     assistant_message_id, requested_config_json, effective_config_json,
     system_prompt_digest, tool_policy_digest, skill_snapshot_digest,
     error_code, error_message, started_at, finished_at, heartbeat_at, created_at,
     retry_of_run_id, retry_client_request_id, speaker_snapshot_json,
     context_snapshot_json, context_snapshot_digest, parent_run_id, root_run_id,
     execution_depth, publish_mode, commit_format_version, system_prompt_snapshot_json
FROM agent_runs;

DROP TABLE agent_runs;
ALTER TABLE agent_runs_v21 RENAME TO agent_runs;

CREATE INDEX ix_agent_runs_session ON agent_runs(session_id, created_at DESC);
CREATE INDEX ix_agent_runs_turn ON agent_runs(turn_id);
CREATE UNIQUE INDEX ux_agent_runs_retry_request
    ON agent_runs(session_id, retry_client_request_id)
    WHERE retry_client_request_id IS NOT NULL;
CREATE INDEX ix_agent_runs_retry_source ON agent_runs(retry_of_run_id);
CREATE UNIQUE INDEX ux_agent_runs_active_top
ON agent_runs(session_id)
WHERE status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children')
  AND parent_run_id IS NULL;

-- The table rebuild dropped triggers created by earlier migrations; recreate
-- the agent_runs execution-fact triggers so format/publish invariants hold for
-- child Runs too.
CREATE TRIGGER agent_runs_commit_format_validate_insert
BEFORE INSERT ON agent_runs
WHEN NEW.commit_format_version NOT IN (1, 2)
BEGIN
    SELECT RAISE(ABORT, 'run_commit_format_invalid');
END;

CREATE TRIGGER agent_runs_commit_format_validate_update
BEFORE UPDATE OF commit_format_version ON agent_runs
WHEN NEW.commit_format_version NOT IN (1, 2)
BEGIN
    SELECT RAISE(ABORT, 'run_commit_format_invalid');
END;

CREATE TRIGGER agent_runs_commit_format_immutable
BEFORE UPDATE OF commit_format_version ON agent_runs
WHEN NEW.commit_format_version <> OLD.commit_format_version
BEGIN
    SELECT RAISE(ABORT, 'run_commit_format_immutable');
END;

CREATE TRIGGER agent_runs_publish_mode_validate_insert
BEFORE INSERT ON agent_runs
WHEN NEW.publish_mode NOT IN ('public_final', 'private_to_parent')
BEGIN
    SELECT RAISE(ABORT, 'run_publish_mode_invalid');
END;

-- 5. Workspace Explorer builtin Role (system-owned, read-only).
INSERT OR IGNORE INTO agent_profiles(id,name,object_kind,handle,scope,icon,color,positioning,draft_json,draft_revision,delegation_enabled,created_at,updated_at)
VALUES('builtin-workspace-explorer','Workspace Explorer','role','workspace-explorer','builtin','folder-search','#0891b2','Inspect workspace files and git history.','{}',0,1,'2026-08-03T00:00:00Z','2026-08-03T00:00:00Z');

INSERT OR IGNORE INTO agent_profile_versions(id,agent_profile_id,version,definition_json,config_digest,status,created_at)
VALUES('builtin-workspace-explorer-v1','builtin-workspace-explorer',1,
  '{"schemaVersion":1,"rolePrompt":"You are the Workspace Explorer. Use read, ls, grep, and find to answer questions about workspace files. You may inspect git history and status with the git_readonly tool (status, diff, log, show, ls-files, blame). Every answer must be concise. End every turn by calling submit_result with a structured result. Never create, modify, or delete files, and never run arbitrary shell commands.",'||
  '"modelBinding":{"mode":"inherit","thinkingEffort":"default","fallbackModelProfileIds":[],"overridableFields":[]},'||
  '"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss",'||
  '"allowedTools":["read","ls","grep","find","git_readonly"],'||
  '"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},'||
  '"delegationPolicy":{"admission":"denied","allowedCallerKinds":["host"],"allowedStrategies":[],"maxInvocationsPerParentRun":1,"maxConcurrentInstances":1,'||
  '"budgetCeiling":{"maxModelCalls":4,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}},'||
  '"outputContract":"text-v1","maxLoopIterations":8}',
  'sha256:47d0049d79260f6f9dd89c35755b8c6e0e5dbdd7ee0a76aeb3df1fb3724c5082','published','2026-08-03T00:00:00Z');

UPDATE agent_profiles SET current_version_id='builtin-workspace-explorer-v1' WHERE id='builtin-workspace-explorer';

INSERT OR IGNORE INTO settings(key,value) VALUES('workspace_explorer_enabled','1');
