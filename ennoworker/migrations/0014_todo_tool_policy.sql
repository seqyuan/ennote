-- 0014: Add todo tool to built-in Discuss policy profile.
--
-- This is a profile data migration — it creates builtin-tool-discuss-v2 with
-- todo in the allowed tools list and updates agent profiles and default
-- settings to reference V2. V1 remains active for queued runs and explicit
-- historical references. Frozen run snapshots are unchanged.
INSERT OR IGNORE INTO policy_profiles
    (id, name, kind, version, config_json, status, created_at, updated_at)
VALUES
    ('builtin-tool-discuss-v2', 'Discuss', 'tool', 2,
     '{"mode":"discuss","allowedTools":["read","ls","grep","find","search_compacted_history","todo"],"maxTimeoutSeconds":300}',
     'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

UPDATE agent_profiles
SET tool_policy_profile_id = 'builtin-tool-discuss-v2'
WHERE tool_policy_profile_id = 'builtin-tool-discuss-v1';

UPDATE settings
SET value = 'builtin-tool-discuss-v2'
WHERE key = 'default_tool_policy_profile_id'
  AND value = 'builtin-tool-discuss-v1';
