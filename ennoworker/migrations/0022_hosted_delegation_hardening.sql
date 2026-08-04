-- Hosted Delegation V1 review hardening. Migration 21 remains immutable; this
-- migration adds missing budget accounting fields and publishes a corrected
-- Workspace Explorer policy as a new immutable Role version.

ALTER TABLE run_budgets ADD COLUMN consumed_output_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE run_budgets ADD COLUMN consumed_cost_usd_micros INTEGER NOT NULL DEFAULT 0;
ALTER TABLE run_budgets ADD COLUMN started_at TEXT;

ALTER TABLE model_profiles ADD COLUMN input_cost_usd_micros_per_million INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_profiles ADD COLUMN output_cost_usd_micros_per_million INTEGER NOT NULL DEFAULT 0;

INSERT OR IGNORE INTO agent_profile_versions
    (id,agent_profile_id,version,definition_json,config_digest,status,created_at)
VALUES(
    'builtin-workspace-explorer-v2',
    'builtin-workspace-explorer',
    2,
    '{"schemaVersion":1,"rolePrompt":"You are the Workspace Explorer. Use read, ls, grep, and find to answer questions about workspace files. You may inspect git history and status with the git_readonly tool (status, diff, log, show, ls-files, blame). Every answer must be concise. End every turn by calling submit_result with a structured result. Never create, modify, or delete files, and never run arbitrary shell commands.","modelBinding":{"mode":"inherit","thinkingEffort":"default","fallbackModelProfileIds":[],"overridableFields":[]},"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss","allowedTools":["read","ls","grep","find","git_readonly"],"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},"delegationPolicy":{"admission":"auto_within_budget","allowedCallerKinds":["host"],"allowedStrategies":["single","parallel"],"maxInvocationsPerParentRun":16,"maxConcurrentInstances":16,"budgetCeiling":{"maxModelCalls":4,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}},"outputContract":"text-v1","maxLoopIterations":8}',
    'sha256:24c22a66689d403447648700e5b26dea5c3361d251002a717f5999db3a8aeddf',
    'published',
    '2026-08-04T00:00:00Z'
);

UPDATE agent_profiles
SET current_version_id='builtin-workspace-explorer-v2',updated_at='2026-08-04T00:00:00Z'
WHERE id='builtin-workspace-explorer';
