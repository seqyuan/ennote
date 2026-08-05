-- 0030: Publish builtin-tool-discuss-v3 with git_readonly in the allowlist.
--
-- RiskClass data-attribute work reclassified git_readonly to RiskReadOnly. In
-- Ask mode it is now allow-listed without approval; the default Discuss profile
-- still excludes it because its allowlist is explicit. v3 adds git_readonly to
-- the v2 allowlist so read-only git inspection works under the default Discuss
-- ceiling without shell or approval.
--
-- v2 stays active for queued Runs and explicit historical references. Frozen
-- Run snapshots and standing approvals are unchanged.
INSERT OR IGNORE INTO policy_profiles
    (id, name, kind, version, config_json, status, created_at, updated_at)
VALUES
    ('builtin-tool-discuss-v3', 'Discuss', 'tool', 3,
     '{"mode":"discuss","allowedTools":["read","ls","grep","find","search_compacted_history","todo","git_readonly"],"maxTimeoutSeconds":300}',
     'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

UPDATE agent_profiles
SET tool_policy_profile_id = 'builtin-tool-discuss-v3'
WHERE tool_policy_profile_id = 'builtin-tool-discuss-v2';

UPDATE settings
SET value = 'builtin-tool-discuss-v3'
WHERE key = 'default_tool_policy_profile_id'
  AND value = 'builtin-tool-discuss-v2';
