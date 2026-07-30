INSERT OR IGNORE INTO policy_profiles
    (id, name, kind, version, config_json, status, created_at, updated_at)
VALUES
    ('builtin-tool-discuss-v1', 'Discuss', 'tool', 1,
     '{"mode":"discuss","allowedTools":["read","ls","grep","find","search_compacted_history"],"maxTimeoutSeconds":300}',
     'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
    ('builtin-tool-auto-v1', 'Auto', 'tool', 1,
     '{"mode":"auto","deniedSubcommands":{"git":["push","clean"]},"allowPipes":true,"allowedWriteRoots":["/workspace"],"maxTimeoutSeconds":300}',
     'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

ALTER TABLE tool_calls ADD COLUMN risk_class TEXT NOT NULL DEFAULT '';
