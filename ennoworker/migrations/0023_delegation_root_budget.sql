-- Item 6 Stage 0: parent/root delegation budget qualification.
-- Closes the Item 5 roadmap gap: every delegation reservation now contends on
-- a root ledger attached to the top-level Host Run, with a frozen delegation
-- policy snapshot in the parent's effective_config_json.

-- 1. Extend policy_profiles.kind to include 'delegation'. SQLite cannot alter a
--    CHECK constraint, so rebuild the table preserving all rows, indexes, and
--    the UNIQUE(kind,name,version) constraint. Migration 23 runs with
--    foreign_keys OFF (see store.Migrate) with a foreign_key_check afterwards.
CREATE TABLE policy_profiles_v23 (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('tool', 'turn', 'vision', 'compaction', 'delegation')),
    version     INTEGER NOT NULL DEFAULT 1,
    config_json TEXT NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(kind, name, version)
);

INSERT INTO policy_profiles_v23 (id,name,kind,version,config_json,status,created_at,updated_at)
    SELECT id,name,kind,version,config_json,status,created_at,updated_at FROM policy_profiles;

DROP TABLE policy_profiles;
ALTER TABLE policy_profiles_v23 RENAME TO policy_profiles;

CREATE INDEX IF NOT EXISTS ix_policy_profiles_kind_status
    ON policy_profiles(kind, status, name, version);

-- 2. Builtin Hosted delegation policy. This is a ceiling, not a grant: every
--    delegation item must also satisfy its Role's delegation budgetCeiling.
INSERT OR IGNORE INTO policy_profiles
    (id, name, kind, version, config_json, status, created_at, updated_at)
VALUES (
    'builtin-hosted-delegation-v1',
    'hosted_delegation',
    'delegation',
    1,
    '{"maxConcurrentChildren":8,"budget":{"maxModelCalls":256,"maxToolCalls":1024,"maxTotalTokens":8000000,"maxOutputTokens":524288,"maxCostUsdMicros":400000000,"maxWallTimeMs":0}}',
    'active',
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
);

-- 3. Session-level policy selection, backfilled to the builtin policy. The
--    digest is computed in Go at freeze time; it is never duplicated in SQL.
ALTER TABLE sessions ADD COLUMN delegation_policy_profile_id TEXT REFERENCES policy_profiles(id);
UPDATE sessions SET delegation_policy_profile_id = 'builtin-hosted-delegation-v1'
WHERE delegation_policy_profile_id IS NULL;

-- 4. Root budget ledger: one row per top-level Host Run. Wall time is not
--    aggregated because parallel children overlap; the wall ceiling stays a
--    per-child concern.
CREATE TABLE delegation_root_budgets (
    root_run_id                  TEXT PRIMARY KEY REFERENCES agent_runs(id),
    policy_snapshot_json         TEXT NOT NULL,
    policy_snapshot_digest       TEXT NOT NULL,
    max_model_calls              INTEGER NOT NULL,
    max_tool_calls               INTEGER NOT NULL,
    max_total_tokens             INTEGER NOT NULL,
    max_output_tokens            INTEGER NOT NULL,
    max_cost_usd_micros          INTEGER NOT NULL,
    max_concurrent_children      INTEGER NOT NULL,
    reserved_model_calls         INTEGER NOT NULL DEFAULT 0,
    reserved_tool_calls          INTEGER NOT NULL DEFAULT 0,
    reserved_total_tokens        INTEGER NOT NULL DEFAULT 0,
    reserved_output_tokens       INTEGER NOT NULL DEFAULT 0,
    reserved_cost_usd_micros     INTEGER NOT NULL DEFAULT 0,
    consumed_model_calls         INTEGER NOT NULL DEFAULT 0,
    consumed_tool_calls          INTEGER NOT NULL DEFAULT 0,
    consumed_total_tokens        INTEGER NOT NULL DEFAULT 0,
    consumed_output_tokens       INTEGER NOT NULL DEFAULT 0,
    consumed_cost_usd_micros     INTEGER NOT NULL DEFAULT 0,
    active_children              INTEGER NOT NULL DEFAULT 0,
    version                      INTEGER NOT NULL DEFAULT 0,
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL
);

-- 5. Idempotency key for root reconciliation: a child budget row is
--    reconciled at most once, keyed by this timestamp.
ALTER TABLE run_budgets ADD COLUMN root_reconciled_at TEXT;
