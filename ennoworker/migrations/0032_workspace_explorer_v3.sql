-- Live qualification finding 2026-08-05: the builtin Workspace Explorer
-- role's budgetCeiling.maxModelCalls=4 is too tight for reasoning models
-- (deepseek-v4-flash needed 5 model calls for an empty-workspace explore).
-- Migration 22 remains immutable; this migration publishes a corrected
-- Workspace Explorer policy as a new immutable Role version (v3).
-- v2 stays current; v3 is referenced explicitly by live tests until the
-- product decision promotes it.

INSERT OR IGNORE INTO agent_profile_versions
    (id,agent_profile_id,version,definition_json,config_digest,status,created_at)
VALUES(
    'builtin-workspace-explorer-v3',
    'builtin-workspace-explorer',
    3,
    '{"schemaVersion":1,"rolePrompt":"You are the Workspace Explorer. Use read, ls, grep, and find to answer questions about workspace files. You may inspect git history and status with the git_readonly tool (status, diff, log, show, ls-files, blame). Every answer must be concise. End every turn by calling submit_result with a structured result. Never create, modify, or delete files, and never run arbitrary shell commands.","modelBinding":{"mode":"inherit","thinkingEffort":"default","fallbackModelProfileIds":[],"overridableFields":[]},"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss","allowedTools":["read","ls","grep","find","git_readonly"],"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},"delegationPolicy":{"admission":"auto_within_budget","allowedCallerKinds":["host"],"allowedStrategies":["single","parallel"],"maxInvocationsPerParentRun":16,"maxConcurrentInstances":16,"budgetCeiling":{"maxModelCalls":6,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}},"outputContract":"text-v1","maxLoopIterations":8}',
    'sha256:c7cf36749030bd0626c24eea7ea325c2b70be64bd2f623b3c94b5fc8b81aa38b',
    'published',
    '2026-08-05T00:00:00Z'
);

UPDATE agent_profiles
SET current_version_id='builtin-workspace-explorer-v3',updated_at='2026-08-05T00:00:00Z'
WHERE id='builtin-workspace-explorer';
