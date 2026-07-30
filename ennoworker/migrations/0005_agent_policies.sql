CREATE TABLE IF NOT EXISTS policy_profiles (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('tool', 'turn', 'vision')),
    version     INTEGER NOT NULL DEFAULT 1,
    config_json TEXT NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(kind, name, version)
);

INSERT INTO policy_profiles (id, name, kind, version, config_json, status, created_at, updated_at)
SELECT 'legacy-tool-' || lower(hex(randomblob(16))), tool_policy, 'tool', 1,
       json_object('mode', 'allow_existing_behavior', 'legacyValue', tool_policy),
       'active', MIN(created_at), MAX(updated_at)
FROM agent_profiles
GROUP BY tool_policy;

INSERT OR IGNORE INTO policy_profiles
    (id, name, kind, version, config_json, status, created_at, updated_at)
VALUES
    ('builtin-tool-allow-existing-v1', 'allow_existing_behavior', 'tool', 1,
     '{"mode":"allow_existing_behavior"}', 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
    ('builtin-turn-fixed-model-v1', 'fixed_model', 'turn', 1,
     '{"mode":"fixed_model","threshold":0.7}', 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
    ('builtin-vision-reject-v1', 'reject', 'vision', 1,
     '{"mode":"reject","maxImageBytes":10485760,"maxPixels":40000000}', 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

ALTER TABLE agent_profiles ADD COLUMN tool_policy_profile_id TEXT REFERENCES policy_profiles(id);
ALTER TABLE agent_profiles ADD COLUMN turn_policy_profile_id TEXT REFERENCES policy_profiles(id);
ALTER TABLE agent_profiles ADD COLUMN vision_policy_profile_id TEXT REFERENCES policy_profiles(id);

UPDATE agent_profiles
SET tool_policy_profile_id = COALESCE(
        (SELECT id FROM policy_profiles p
         WHERE p.kind = 'tool' AND p.name = agent_profiles.tool_policy AND p.version = 1 LIMIT 1),
        'builtin-tool-allow-existing-v1'),
    turn_policy_profile_id = 'builtin-turn-fixed-model-v1',
    vision_policy_profile_id = 'builtin-vision-reject-v1';

INSERT OR IGNORE INTO settings (key, value) VALUES
    ('default_tool_policy_profile_id', 'builtin-tool-allow-existing-v1'),
    ('default_turn_policy_profile_id', 'builtin-turn-fixed-model-v1'),
    ('default_vision_policy_profile_id', 'builtin-vision-reject-v1');

ALTER TABLE model_calls ADD COLUMN purpose TEXT NOT NULL DEFAULT 'agent_turn';
ALTER TABLE model_calls ADD COLUMN route_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN parent_iteration INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_calls ADD COLUMN source_artifact_id TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS ux_model_calls_run_iteration_attempt;
CREATE UNIQUE INDEX IF NOT EXISTS ux_model_calls_run_iteration_purpose_attempt_source
    ON model_calls(run_id, iteration, purpose, attempt, source_artifact_id);

ALTER TABLE tool_calls ADD COLUMN original_arguments_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE tool_calls ADD COLUMN effective_arguments_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE tool_calls ADD COLUMN policy_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_calls ADD COLUMN policy_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tool_calls ADD COLUMN policy_action TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_calls ADD COLUMN policy_code TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_calls ADD COLUMN raw_result_preview TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_calls ADD COLUMN raw_result_path TEXT;
ALTER TABLE tool_calls ADD COLUMN projected_result_preview TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_calls ADD COLUMN projected_result_path TEXT;
ALTER TABLE tool_calls ADD COLUMN stop_after_batch INTEGER NOT NULL DEFAULT 0;

UPDATE tool_calls
SET original_arguments_json = arguments_json,
    effective_arguments_json = arguments_json,
    projected_result_preview = result_preview;

CREATE INDEX IF NOT EXISTS ix_policy_profiles_kind_status
    ON policy_profiles(kind, status, name, version);
