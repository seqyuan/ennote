-- 0033: Agent Flow substrate (roadmap item 7, Phase 1).
--
-- Governance: `2026-08-05-agent-flow-design-v2.md` (Approved). The lifecycle
-- mirrors the MCP profile/binding/run-snapshot pattern: immutable versioned
-- profiles are the authoring transport; project bindings are desired enablement;
-- run records freeze the manifest digest + task checkpoints. The meta-Run is a
-- pure orchestration state machine; every task runs as a standard child Run
-- through the existing delegation substrate (groups/items/attempts/budgets).

-- Stable flow identity (slug unique per source kind + project scope).
CREATE TABLE IF NOT EXISTS agent_flow_profiles (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    slug          TEXT NOT NULL,
    source_kind   TEXT NOT NULL CHECK (source_kind IN ('managed', 'project_file')),
    project_scope TEXT,               -- NULL for managed; project_file owner project
    source_locator TEXT,              -- .ennote/agent-flows/<name>.yaml path
    lifecycle_status TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle_status IN ('active', 'archived')),
    latest_version INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    UNIQUE(slug, source_kind, project_scope)
);

-- Immutable published flow versions. config_digest is unique per profile so
-- candidate discovery can reuse an existing immutable version (never rewrite).
CREATE TABLE IF NOT EXISTS agent_flow_versions (
    id              TEXT PRIMARY KEY,
    profile_id      TEXT NOT NULL REFERENCES agent_flow_profiles(id),
    version         INTEGER NOT NULL,
    config_digest   TEXT NOT NULL,
    definition_json TEXT NOT NULL,
    published_at    TEXT NOT NULL,
    UNIQUE(profile_id, version),
    UNIQUE(profile_id, config_digest)
);

-- Per-Project desired enablement of one flow version.
CREATE TABLE IF NOT EXISTS project_agent_flow_bindings (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL,
    flow_version_id TEXT NOT NULL REFERENCES agent_flow_versions(id),
    desired_enabled INTEGER NOT NULL DEFAULT 0,
    revision        INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE(project_id, flow_version_id)
);

-- Meta-Run record. run_id is the anchor top-level agent run id: delegation
-- children, durable events, and SSE all hang off it.
CREATE TABLE IF NOT EXISTS run_agent_flow (
    run_id            TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL,
    project_id        TEXT NOT NULL,
    flow_version_id   TEXT NOT NULL REFERENCES agent_flow_versions(id),
    manifest_digest   TEXT NOT NULL,
    inputs_json       TEXT NOT NULL DEFAULT '{}',
    state             TEXT NOT NULL DEFAULT 'pending'
                      CHECK (state IN ('pending','running','completed','failed','cancelled',
                                       'convergence_exceeded','budget_exceeded')),
    total_tokens_used INTEGER NOT NULL DEFAULT 0,
    terminal_reason   TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    finished_at       TEXT
);

-- One row per task: the frozen task snapshot (role version, skill digests,
-- goal digest + resolved text, budget) and the task checkpoint
-- (terminal_state + output_ref = flow checkpoint; completed tasks are never
-- replayed on resume).
CREATE TABLE IF NOT EXISTS run_agent_flow_nodes (
    run_id          TEXT NOT NULL REFERENCES run_agent_flow(run_id),
    task_index      INTEGER NOT NULL,
    handle          TEXT NOT NULL,
    role_version_id TEXT,
    skill_digests_json TEXT NOT NULL DEFAULT '[]',
    goal_digest     TEXT,
    goal_text       TEXT,
    budget_json     TEXT NOT NULL DEFAULT '{}',
    terminal_state  TEXT NOT NULL DEFAULT 'pending'
                    CHECK (terminal_state IN ('pending','running','completed','failed',
                                              'blocked','cancelled','interrupted')),
    output_ref      TEXT,
    child_run_id    TEXT,
    error_code      TEXT,
    created_at      TEXT NOT NULL,
    finished_at     TEXT,
    PRIMARY KEY (run_id, task_index)
);

CREATE INDEX IF NOT EXISTS idx_run_agent_flow_nodes_child ON run_agent_flow_nodes(child_run_id);
CREATE INDEX IF NOT EXISTS idx_project_agent_flow_bindings_project ON project_agent_flow_bindings(project_id);
CREATE INDEX IF NOT EXISTS idx_agent_flow_versions_profile ON agent_flow_versions(profile_id);
