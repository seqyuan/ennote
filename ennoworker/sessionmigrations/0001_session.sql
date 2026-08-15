CREATE TABLE "agent_runs" (
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
    system_prompt_snapshot_json  TEXT NOT NULL DEFAULT '{}', source_completion_id TEXT,
    CHECK((run_kind = 'agent' AND turn_id IS NOT NULL) OR
          (run_kind = 'context_compaction' AND turn_id IS NULL AND base_message_id IS NOT NULL) OR
          (run_kind = 'delegated_agent' AND parent_run_id IS NOT NULL))
);
CREATE TABLE artifacts (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL,
    session_id     TEXT,
    message_id     TEXT,
    run_id         TEXT,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL DEFAULT 'file',
    mime_type      TEXT NOT NULL DEFAULT 'application/octet-stream',
    storage_path   TEXT NOT NULL,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    sha256         TEXT NOT NULL DEFAULT '',
    metadata_json  TEXT NOT NULL DEFAULT '{}',
    created_at     TEXT NOT NULL
, source_tool_call_id TEXT NOT NULL DEFAULT '', source_kind TEXT NOT NULL DEFAULT 'upload', source_workspace_path TEXT NOT NULL DEFAULT '', retention_class TEXT NOT NULL DEFAULT 'project');
CREATE TABLE attention_items (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL,
    session_id        TEXT NOT NULL REFERENCES sessions(id),
    source_kind       TEXT NOT NULL CHECK(source_kind IN (
                          'tool_approval','delegation_approval','delegation_item','delegation_completion')),
    source_id         TEXT NOT NULL,
    source_generation INTEGER NOT NULL DEFAULT 0,
    kind              TEXT NOT NULL CHECK(kind IN (
                          'approval_required','needs_input','delegation_completed','delegation_failed')),
    requires_action   INTEGER NOT NULL CHECK(requires_action IN (0,1)),
    status            TEXT NOT NULL CHECK(status IN ('pending','resolved','dismissed')),
    display_json      TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    resolved_at       TEXT,
    dismissed_at      TEXT,
    UNIQUE(source_kind, source_id, source_generation, kind)
);
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
CREATE TABLE delegation_approval_requests (
    id                         TEXT PRIMARY KEY,
    group_id                   TEXT NOT NULL REFERENCES delegation_groups(id),
    generation                 INTEGER NOT NULL,
    kind                       TEXT NOT NULL CHECK(kind IN ('retry_budget')),
    parent_run_id              TEXT NOT NULL REFERENCES agent_runs(id),
    session_id                 TEXT NOT NULL REFERENCES sessions(id),
    status                     TEXT NOT NULL CHECK(status IN ('pending','approved','rejected','cancelled')),
    items_json                 TEXT NOT NULL,
    decision_client_request_id TEXT,
    requested_at               TEXT NOT NULL,
    resolved_at                TEXT,
    UNIQUE(group_id, generation, kind)
);
CREATE TABLE delegation_attempt_continuations (
    attempt_id        TEXT PRIMARY KEY REFERENCES delegation_item_attempts(id),
    source_attempt_id TEXT NOT NULL REFERENCES delegation_item_attempts(id),
    kind              TEXT NOT NULL CHECK(kind IN ('input','follow_up')),
    input_json        TEXT NOT NULL,
    input_digest      TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    CHECK(attempt_id <> source_attempt_id)
);
CREATE TABLE delegation_completions (
    id                    TEXT PRIMARY KEY,
    handle_id             TEXT NOT NULL REFERENCES delegation_handles(id),
    session_id            TEXT NOT NULL REFERENCES sessions(id),
    generation            INTEGER NOT NULL,
    kind                  TEXT NOT NULL CHECK(kind IN ('completed','partial','failed','cancelled')),
    result_json           TEXT NOT NULL,
    result_digest         TEXT NOT NULL,
    sequence              INTEGER NOT NULL,
    delivery_status       TEXT NOT NULL CHECK(delivery_status IN (
                              'pending','consumed_by_parent','notified','resume_queued','resume_completed')),
    resume_run_id         TEXT REFERENCES agent_runs(id),
    created_at            TEXT NOT NULL,
    delivered_at          TEXT,
    UNIQUE(handle_id, generation),
    UNIQUE(session_id, sequence)
);
CREATE TABLE delegation_group_generations (
    id                            TEXT PRIMARY KEY,
    group_id                      TEXT NOT NULL REFERENCES delegation_groups(id),
    generation                    INTEGER NOT NULL,
    kind                          TEXT NOT NULL CHECK(kind IN ('initial','retry','input','follow_up')),
    status                        TEXT NOT NULL CHECK(status IN (
                                      'awaiting_authorization','queued','running','settled','failed','cancelled')),
    retry_selection_json          TEXT NOT NULL DEFAULT '[]',
    reused_attempts_json          TEXT NOT NULL DEFAULT '[]',
    authorization_snapshot_json   TEXT NOT NULL,
    authorization_snapshot_digest TEXT NOT NULL,
    budget_snapshot_json          TEXT NOT NULL,
    budget_snapshot_digest        TEXT NOT NULL,
    client_request_id             TEXT NOT NULL,
    created_at                    TEXT NOT NULL,
    completed_at                  TEXT, request_digest TEXT,
    UNIQUE(group_id, generation),
    UNIQUE(group_id, client_request_id)
);
CREATE TABLE delegation_groups (
    id                  TEXT PRIMARY KEY,
    parent_run_id       TEXT NOT NULL REFERENCES agent_runs(id),
    parent_tool_call_id TEXT NOT NULL,
    strategy            TEXT NOT NULL DEFAULT 'single',
    status              TEXT NOT NULL DEFAULT 'pending',
    created_at          TEXT NOT NULL, current_generation INTEGER NOT NULL DEFAULT 0, updated_at TEXT, completed_at TEXT,
    UNIQUE(parent_run_id, parent_tool_call_id)
);
CREATE TABLE delegation_handles (
    id                    TEXT PRIMARY KEY,
    group_id              TEXT NOT NULL UNIQUE REFERENCES delegation_groups(id),
    session_id            TEXT NOT NULL REFERENCES sessions(id),
    source_parent_run_id  TEXT NOT NULL REFERENCES agent_runs(id),
    source_branch_id      TEXT NOT NULL REFERENCES session_branches(id),
    execution_mode        TEXT NOT NULL CHECK(execution_mode IN ('blocking','background')),
    auto_resume           INTEGER NOT NULL DEFAULT 0 CHECK(auto_resume IN (0,1)),
    status                TEXT NOT NULL CHECK(status IN ('active','completed','cancelled')),
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);
CREATE TABLE delegation_item_attempts (
    id                            TEXT PRIMARY KEY,
    item_id                       TEXT NOT NULL REFERENCES delegation_items(id),
    generation                    INTEGER NOT NULL,
    retry_of_attempt_id           TEXT REFERENCES delegation_item_attempts(id),
    child_run_id                  TEXT NOT NULL UNIQUE REFERENCES agent_runs(id),
    authorization_snapshot_json   TEXT NOT NULL,
    authorization_snapshot_digest TEXT NOT NULL,
    reserved_budget_json          TEXT NOT NULL,
    actual_usage_json             TEXT NOT NULL DEFAULT '{}',
    status                        TEXT NOT NULL CHECK(status IN (
                                      'queued','running','succeeded','blocked','needs_input',
                                      'not_authorized','failed','cancelled','interrupted')),
    terminal_kind                 TEXT,
    result_json                   TEXT,
    result_digest                 TEXT,
    error_code                    TEXT,
    error_message                 TEXT,
    root_reconciled_at            TEXT,
    created_at                    TEXT NOT NULL,
    started_at                    TEXT,
    finished_at                   TEXT,
    UNIQUE(item_id, generation)
);
CREATE TABLE delegation_items (
    id               TEXT PRIMARY KEY,
    group_id         TEXT NOT NULL REFERENCES delegation_groups(id),
    child_run_id     TEXT REFERENCES agent_runs(id),
    name             TEXT NOT NULL,
    role_version_id  TEXT NOT NULL,
    assignment_json  TEXT NOT NULL DEFAULT '{}',
    output_contract  TEXT NOT NULL DEFAULT 'text-v1',
    budget_json      TEXT NOT NULL DEFAULT '{}',
    result_json      TEXT,
    status           TEXT NOT NULL DEFAULT 'pending',
    ordinal          INTEGER NOT NULL,
    created_at       TEXT NOT NULL, depends_json TEXT NOT NULL DEFAULT '[]', skills_json TEXT NOT NULL DEFAULT '[]',
    role_meta_json TEXT NOT NULL DEFAULT '{}',
    UNIQUE(group_id, ordinal),
    UNIQUE(child_run_id)
);
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
CREATE TABLE delivery_events (
    event_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id        TEXT NOT NULL REFERENCES sessions(id),
    source_kind       TEXT NOT NULL,
    source_id         TEXT NOT NULL,
    source_generation INTEGER NOT NULL,
    event_type        TEXT NOT NULL,
    payload_json      TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    UNIQUE(source_kind, source_id, source_generation, event_type)
);
CREATE TABLE image_descriptions (
    id                          TEXT PRIMARY KEY,
    artifact_id                 TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    artifact_sha256             TEXT NOT NULL,
    descriptor_model_profile_id TEXT NOT NULL,
    actual_model                TEXT NOT NULL DEFAULT '',
    prompt_version              TEXT NOT NULL,
    description                 TEXT NOT NULL,
    description_sha256          TEXT NOT NULL,
    model_call_id               TEXT REFERENCES model_calls(id),
    created_at                  TEXT NOT NULL,
    UNIQUE(artifact_sha256, descriptor_model_profile_id, prompt_version)
);
CREATE TABLE mcp_requests (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL,
    run_server_id       TEXT NOT NULL REFERENCES run_mcp_servers(id),
    run_tool_id         TEXT NOT NULL REFERENCES run_mcp_tools(id),
    tool_call_id        TEXT NOT NULL,
    connection_generation INTEGER NOT NULL,
    protocol_request_id TEXT,
    status              TEXT NOT NULL CHECK (status IN ('planned','dispatched','completed','failed','cancelled','outcome_unknown')),
    request_digest      TEXT NOT NULL,
    response_digest     TEXT,
    error_code          TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE(run_id, tool_call_id)
);
CREATE TABLE message_parts (
    id           TEXT PRIMARY KEY,
    message_id   TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    ordinal      INTEGER NOT NULL,
    block_kind   TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    UNIQUE(message_id, ordinal)
);
CREATE TABLE messages (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    parent_message_id TEXT,
    role              TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'complete',
    run_id            TEXT,
    created_at        TEXT NOT NULL, speaker_kind TEXT NOT NULL DEFAULT 'host', speaker_object_id TEXT, speaker_version_id TEXT, participant_instance_id TEXT REFERENCES room_member_instances(id), speaker_snapshot_json TEXT NOT NULL DEFAULT '{}', addressee_kind TEXT, addressee_object_id TEXT, addressee_version_id TEXT, reply_to_message_id TEXT REFERENCES messages(id), visibility TEXT NOT NULL DEFAULT 'public', originated_at TEXT,
    UNIQUE(session_id, id),
    FOREIGN KEY(session_id, parent_message_id)
        REFERENCES messages(session_id, id)
);
CREATE TABLE model_calls (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq                 INTEGER NOT NULL,
    provider_profile_id TEXT,
    model_profile_id    TEXT,
    actual_model        TEXT NOT NULL DEFAULT '',
    requested_config_json TEXT NOT NULL DEFAULT '{}',
    effective_config_json TEXT NOT NULL DEFAULT '{}',
    stop_reason         TEXT,
    http_status         INTEGER,
    error_code          TEXT,
    input_tokens        INTEGER NOT NULL DEFAULT 0,
    output_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens    INTEGER NOT NULL DEFAULT 0,
    started_at          TEXT NOT NULL,
    finished_at         TEXT
, iteration INTEGER NOT NULL DEFAULT 0, attempt INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'started', purpose TEXT NOT NULL DEFAULT 'agent_turn', route_reason TEXT NOT NULL DEFAULT '', parent_iteration INTEGER NOT NULL DEFAULT 0, source_artifact_id TEXT NOT NULL DEFAULT '', request_generation INTEGER NOT NULL DEFAULT 0, compaction_id TEXT REFERENCES context_compactions(id) ON DELETE SET NULL);
CREATE TABLE room_member_instances (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL,
    created_at TEXT NOT NULL, role_version_id TEXT,
    UNIQUE(session_id, id)
);
CREATE TABLE run_agent_flow (
    run_id            TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL,
    project_id        TEXT NOT NULL,
    flow_version_id   TEXT NOT NULL,
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
, cancel_requested INTEGER NOT NULL DEFAULT 0, convergence_rounds_json TEXT NOT NULL DEFAULT '{}',
    definition_json TEXT NOT NULL DEFAULT '{}', config_digest TEXT NOT NULL DEFAULT '');
CREATE TABLE run_agent_flow_check_approvals (
    run_id     TEXT NOT NULL REFERENCES run_agent_flow(run_id),
    task_index INTEGER NOT NULL,
    command    TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'approved', 'rejected')),
    decision_client_request_id TEXT,
    requested_at TEXT NOT NULL,
    resolved_at  TEXT,
    PRIMARY KEY (run_id, task_index)
);
CREATE TABLE run_agent_flow_nodes (
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
    finished_at     TEXT, child_run_ids_json TEXT NOT NULL DEFAULT '[]', read_only INTEGER NOT NULL DEFAULT 0, writes_json TEXT NOT NULL DEFAULT '[]',
    role_definition_json TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, task_index)
);
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
, consumed_output_tokens INTEGER NOT NULL DEFAULT 0, consumed_cost_usd_micros INTEGER NOT NULL DEFAULT 0, started_at TEXT, root_reconciled_at TEXT);
CREATE TABLE run_context_compactions (
    id                         TEXT PRIMARY KEY,
    run_id                     TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    previous_compaction_id     TEXT REFERENCES run_context_compactions(id) ON DELETE SET NULL,
    status                     TEXT NOT NULL CHECK(status IN ('planned','running','completed','failed','cancelled')),
    reason                     TEXT NOT NULL CHECK(reason IN ('threshold','overflow')),
    iteration                  INTEGER NOT NULL CHECK(iteration > 1),
    request_generation         INTEGER NOT NULL CHECK(request_generation >= 0),
    policy_profile_id          TEXT,
    policy_version             INTEGER,
    effective_config_json      TEXT NOT NULL DEFAULT '{}',
    source_digest              TEXT NOT NULL,
    summary_contract_digest    TEXT NOT NULL,
    summary                    TEXT NOT NULL DEFAULT '',
    summary_digest             TEXT NOT NULL DEFAULT '',
    covered_generated          INTEGER NOT NULL DEFAULT 0 CHECK(covered_generated >= 0),
    model_call_id              TEXT REFERENCES model_calls(id) ON DELETE SET NULL,
    tokens_before              INTEGER NOT NULL DEFAULT 0,
    estimated_tokens_after     INTEGER NOT NULL DEFAULT 0,
    reclaimed_tokens           INTEGER NOT NULL DEFAULT 0,
    error_code                 TEXT,
    error_message              TEXT,
    started_at                 TEXT,
    finished_at                TEXT,
    created_at                 TEXT NOT NULL,
    CHECK(status <> 'completed' OR (
        summary <> '' AND summary_digest <> '' AND model_call_id IS NOT NULL AND finished_at IS NOT NULL
    ))
);
CREATE TABLE run_events (
    event_id     INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    event_type   TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL
);
CREATE TABLE run_execution_checkpoints (
    id             TEXT PRIMARY KEY,
    run_id         TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL,
    iteration      INTEGER NOT NULL,
    batch_digest   TEXT NOT NULL,
    state_json     TEXT NOT NULL,
    status         TEXT NOT NULL CHECK(status IN ('pending','executing','consumed','cancelled','interrupted')),
    created_at     TEXT NOT NULL,
    started_at     TEXT,
    finished_at    TEXT
);
CREATE TABLE run_input_queue (
    id                TEXT PRIMARY KEY,
    run_id            TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    client_request_id TEXT NOT NULL,
    seq               INTEGER NOT NULL,
    kind              TEXT NOT NULL CHECK(kind IN ('steer', 'follow_up')),
    content_json      TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'queued'
                      CHECK(status IN ('queued', 'injected', 'cancelled')),
    created_at        TEXT NOT NULL,
    injected_at       TEXT,
    cancelled_at      TEXT,
    UNIQUE(run_id, client_request_id),
    UNIQUE(run_id, seq)
);
CREATE TABLE run_mcp_servers (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL,
    binding_id          TEXT NOT NULL,
    binding_revision    INTEGER NOT NULL,
    profile_version_id  TEXT NOT NULL,
    config_digest       TEXT NOT NULL,
    negotiated_protocol TEXT NOT NULL,
    server_identity_digest TEXT NOT NULL,
    catalog_digest      TEXT NOT NULL,
    required            INTEGER NOT NULL,
    unavailable_reason  TEXT,
    connection_generation INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL
);
CREATE TABLE run_mcp_tools (
    id               TEXT PRIMARY KEY,
    run_server_id    TEXT NOT NULL REFERENCES run_mcp_servers(id),
    remote_name      TEXT NOT NULL,
    exposed_name     TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    input_schema_json TEXT NOT NULL,
    output_schema_json TEXT,
    schema_digest    TEXT NOT NULL,
    risk_class       TEXT NOT NULL,
    execution_class  TEXT NOT NULL,
    source_kind      TEXT NOT NULL,
    UNIQUE(run_server_id, remote_name)
);
CREATE TABLE run_messages (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    role TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    visibility TEXT NOT NULL DEFAULT 'private',
    created_at TEXT NOT NULL,
    UNIQUE(run_id, ordinal)
);
CREATE TABLE session_branches (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    parent_branch_id  TEXT REFERENCES session_branches(id) ON DELETE SET NULL,
    fork_message_id   TEXT,
    leaf_message_id   TEXT,
    label             TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    FOREIGN KEY(session_id, fork_message_id)
        REFERENCES messages(session_id, id),
    FOREIGN KEY(session_id, leaf_message_id)
        REFERENCES messages(session_id, id)
);
CREATE TABLE session_compaction_state (
    session_id                    TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    failure_cooldown_until        TEXT,
    last_failure_code             TEXT,
    ineffective_count             INTEGER NOT NULL DEFAULT 0,
    last_reclaim_ratio            REAL,
    updated_at                    TEXT NOT NULL
);
CREATE TABLE session_delivery_sequences (
    session_id    TEXT PRIMARY KEY REFERENCES sessions(id),
    next_sequence INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE sessions (
    id                         TEXT PRIMARY KEY,
    project_id                 TEXT NOT NULL,
    title                      TEXT NOT NULL DEFAULT 'New Session',
    status                     TEXT NOT NULL DEFAULT 'active',
    active_leaf_message_id     TEXT,
    default_agent_profile_id   TEXT,
    default_model_profile_id   TEXT,
    source_session_id          TEXT,
    source_message_id          TEXT,
    created_at                 TEXT NOT NULL,
    updated_at                 TEXT NOT NULL
, compaction_policy_profile_id TEXT, active_branch_id TEXT REFERENCES session_branches(id), mode TEXT NOT NULL DEFAULT 'hosted', delegation_policy_profile_id TEXT);
CREATE TABLE skill_snapshots (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    skill_id      TEXT NOT NULL,
    version       TEXT NOT NULL DEFAULT '',
    manifest_digest TEXT NOT NULL DEFAULT '',
    content_digest TEXT NOT NULL DEFAULT '',
    snapshot_path TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
, rel_path TEXT NOT NULL DEFAULT '');
CREATE TABLE standing_approval_candidates (
    approval_id   TEXT NOT NULL REFERENCES tool_approval_requests(id) ON DELETE CASCADE,
    call_index    INTEGER NOT NULL CHECK (call_index >= 0),
    tool_call_id  TEXT NOT NULL,
    tool_name     TEXT NOT NULL,
    scope_kind    TEXT NOT NULL,
    scope_version INTEGER NOT NULL CHECK (scope_version >= 1),
    scope_key     TEXT NOT NULL,
    scope_display TEXT NOT NULL,
    risk_class    TEXT NOT NULL CHECK (risk_class = 'external'),
    PRIMARY KEY (approval_id, call_index),
    CHECK (length(tool_name) BETWEEN 1 AND 128),
    CHECK (length(scope_kind) BETWEEN 1 AND 64),
    CHECK (length(scope_key) BETWEEN 1 AND 512),
    CHECK (length(scope_display) BETWEEN 1 AND 200)
);
CREATE TABLE standing_approval_grants (
    approval_id TEXT NOT NULL,
    call_index  INTEGER NOT NULL,
    rule_id     TEXT NOT NULL REFERENCES standing_approvals(id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (approval_id, call_index),
    FOREIGN KEY (approval_id, call_index)
        REFERENCES standing_approval_candidates(approval_id, call_index)
        ON DELETE CASCADE
);
CREATE TABLE standing_approvals (
    id                       TEXT PRIMARY KEY,
    session_id               TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tool_name                TEXT NOT NULL,
    scope_kind               TEXT NOT NULL,
    scope_version            INTEGER NOT NULL CHECK (scope_version >= 1),
    scope_key                TEXT NOT NULL,
    scope_display            TEXT NOT NULL,
    risk_class               TEXT NOT NULL CHECK (risk_class = 'external'),
    created_at               TEXT NOT NULL,
    created_by_run_id        TEXT NOT NULL,
    created_by_approval_id   TEXT NOT NULL,
    revoked_at               TEXT,
    revoke_client_request_id TEXT NOT NULL DEFAULT '',
    CHECK (length(tool_name) BETWEEN 1 AND 128),
    CHECK (length(scope_kind) BETWEEN 1 AND 64),
    CHECK (length(scope_key) BETWEEN 1 AND 512),
    CHECK (length(scope_display) BETWEEN 1 AND 200)
);
CREATE TABLE tool_approval_requests (
    id                         TEXT PRIMARY KEY,
    run_id                     TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    session_id                 TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    checkpoint_id              TEXT NOT NULL UNIQUE REFERENCES run_execution_checkpoints(id) ON DELETE CASCADE,
    iteration                  INTEGER NOT NULL,
    batch_digest               TEXT NOT NULL,
    status                     TEXT NOT NULL CHECK(status IN ('pending','approved','rejected','cancelled')),
    items_json                 TEXT NOT NULL,
    decision_client_request_id TEXT,
    requested_at               TEXT NOT NULL,
    resolved_at                TEXT
);
CREATE TABLE tool_calls (
    id                TEXT PRIMARY KEY,
    run_id            TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq               INTEGER NOT NULL,
    tool_call_id      TEXT NOT NULL,
    tool_name         TEXT NOT NULL,
    arguments_json    TEXT NOT NULL DEFAULT '{}',
    status            TEXT NOT NULL DEFAULT 'pending',
    result_preview    TEXT NOT NULL DEFAULT '',
    result_path       TEXT,
    is_error          INTEGER NOT NULL DEFAULT 0,
    exit_code         INTEGER,
    started_at        TEXT NOT NULL,
    finished_at       TEXT
, iteration INTEGER NOT NULL DEFAULT 0, call_index INTEGER NOT NULL DEFAULT 0, arguments_fragment TEXT, original_arguments_json TEXT NOT NULL DEFAULT '{}', effective_arguments_json TEXT NOT NULL DEFAULT '{}', policy_id TEXT NOT NULL DEFAULT '', policy_version INTEGER NOT NULL DEFAULT 0, policy_action TEXT NOT NULL DEFAULT '', policy_code TEXT NOT NULL DEFAULT '', raw_result_preview TEXT NOT NULL DEFAULT '', raw_result_path TEXT, projected_result_preview TEXT NOT NULL DEFAULT '', projected_result_path TEXT, stop_after_batch INTEGER NOT NULL DEFAULT 0, risk_class TEXT NOT NULL DEFAULT '', raw_artifact_refs_json TEXT NOT NULL DEFAULT '[]', projected_artifact_refs_json TEXT NOT NULL DEFAULT '[]', standing_rule_id TEXT NOT NULL DEFAULT '');
CREATE TABLE "turns" (
    id                           TEXT PRIMARY KEY,
    session_id                   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    client_request_id            TEXT NOT NULL,
    user_message_id              TEXT NOT NULL REFERENCES messages(id),
    base_message_id              TEXT,
    status                       TEXT NOT NULL DEFAULT 'pending',
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL,
    input_message_id             TEXT REFERENCES messages(id),
    input_kind                   TEXT NOT NULL DEFAULT 'user_message'
                                     CHECK(input_kind IN ('user_message','room_control','delegation_completion')),
    target_kind                  TEXT NOT NULL DEFAULT 'host',
    target_object_id             TEXT,
    target_version_id            TEXT,
    target_participant_instance_id TEXT REFERENCES room_member_instances(id),
    context_mode                 TEXT NOT NULL DEFAULT 'room',
    reply_to_json                TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE usage_records (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    ref_id      TEXT NOT NULL,
    details_json TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL
);
CREATE INDEX idx_artifacts_run_tool
    ON artifacts(run_id, source_tool_call_id, created_at);
CREATE INDEX idx_artifacts_session_created
    ON artifacts(session_id, created_at, id);
CREATE INDEX idx_mcp_requests_run ON mcp_requests(run_id);
CREATE INDEX idx_run_agent_flow_nodes_child ON run_agent_flow_nodes(child_run_id);
CREATE INDEX idx_run_mcp_servers_run ON run_mcp_servers(run_id);
CREATE INDEX idx_run_mcp_tools_server ON run_mcp_tools(run_server_id);
CREATE INDEX idx_sessions_project_status_updated
    ON sessions(project_id, status, updated_at DESC, id);
CREATE INDEX ix_agent_runs_retry_source ON agent_runs(retry_of_run_id);
CREATE INDEX ix_agent_runs_session ON agent_runs(session_id, created_at DESC);
CREATE INDEX ix_agent_runs_turn ON agent_runs(turn_id);
CREATE INDEX ix_artifacts_kind_sha256
    ON artifacts(kind, sha256);
CREATE INDEX ix_artifacts_project ON artifacts(project_id);
CREATE INDEX ix_artifacts_session ON artifacts(session_id);
CREATE INDEX ix_attention_pending ON attention_items(status, project_id, created_at);
CREATE INDEX ix_attention_session ON attention_items(session_id, status, created_at);
CREATE INDEX ix_context_compactions_base_leaf
    ON context_compactions(session_id, base_leaf_message_id, status);
CREATE INDEX ix_context_compactions_previous
    ON context_compactions(previous_compaction_id);
CREATE INDEX ix_context_compactions_reuse
    ON context_compactions(session_id, source_digest, summary_contract_digest, status);
CREATE INDEX ix_context_compactions_session_completed
    ON context_compactions(session_id, status, created_at DESC);
CREATE INDEX ix_delegation_approvals_pending ON delegation_approval_requests(status, session_id);
CREATE INDEX ix_delegation_attempts_child ON delegation_item_attempts(child_run_id);
CREATE INDEX ix_delegation_attempts_item ON delegation_item_attempts(item_id, generation);
CREATE INDEX ix_delegation_generations_group ON delegation_group_generations(group_id, generation);
CREATE INDEX ix_delegation_items_group ON delegation_items(group_id);
CREATE INDEX ix_delivery_events_session ON delivery_events(session_id, event_id);
CREATE INDEX ix_execution_checkpoints_run
    ON run_execution_checkpoints(run_id, created_at DESC);
CREATE INDEX ix_image_descriptions_artifact
    ON image_descriptions(artifact_id, created_at);
CREATE INDEX ix_message_parts_message ON message_parts(message_id, ordinal);
CREATE INDEX ix_messages_parent ON messages(parent_message_id);
CREATE INDEX ix_messages_session ON messages(session_id, created_at);
CREATE INDEX ix_model_calls_run ON model_calls(run_id, seq);
CREATE INDEX ix_run_context_compactions_previous
    ON run_context_compactions(previous_compaction_id);
CREATE INDEX ix_run_context_compactions_run
    ON run_context_compactions(run_id,created_at,id);
CREATE INDEX ix_run_events_run ON run_events(run_id, seq);
CREATE INDEX ix_run_input_queue_pending
    ON run_input_queue(run_id, kind, status, seq);
CREATE INDEX ix_run_messages_run_ordinal ON run_messages(run_id, ordinal);
CREATE INDEX ix_session_branches_parent
    ON session_branches(parent_branch_id);
CREATE INDEX ix_session_branches_session
    ON session_branches(session_id, created_at, id);
CREATE INDEX ix_sessions_project ON sessions(project_id, updated_at DESC);
CREATE INDEX ix_skill_snapshots_run ON skill_snapshots(run_id);
CREATE INDEX ix_standing_approvals_session_active
    ON standing_approvals(session_id, created_at DESC)
    WHERE revoked_at IS NULL;
CREATE INDEX ix_tool_approvals_run
    ON tool_approval_requests(run_id, requested_at DESC);
CREATE INDEX ix_tool_approvals_session
    ON tool_approval_requests(session_id, requested_at DESC);
CREATE INDEX ix_tool_calls_run ON tool_calls(run_id, seq);
CREATE UNIQUE INDEX ux_agent_runs_active_top
ON agent_runs(session_id)
WHERE status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children')
  AND parent_run_id IS NULL;
CREATE UNIQUE INDEX ux_agent_runs_retry_request
    ON agent_runs(session_id, retry_client_request_id)
    WHERE retry_client_request_id IS NOT NULL;
CREATE UNIQUE INDEX ux_agent_runs_source_completion
    ON agent_runs(source_completion_id)
    WHERE source_completion_id IS NOT NULL;
CREATE UNIQUE INDEX ux_context_compactions_one_active
    ON context_compactions(session_id) WHERE status IN ('planned', 'running');
CREATE UNIQUE INDEX ux_execution_checkpoint_pending
    ON run_execution_checkpoints(run_id)
    WHERE status IN ('pending', 'executing');
CREATE UNIQUE INDEX ux_model_calls_run_generation_iteration_purpose_attempt_source
    ON model_calls(run_id, request_generation, iteration, purpose, attempt, source_artifact_id);
CREATE UNIQUE INDEX ux_room_members_role_version
ON room_member_instances(session_id, role_id, role_version_id)
WHERE role_version_id IS NOT NULL;
CREATE UNIQUE INDEX ux_run_context_compactions_one_active
    ON run_context_compactions(run_id) WHERE status IN ('planned','running');
CREATE UNIQUE INDEX ux_run_context_compactions_reuse
    ON run_context_compactions(run_id,source_digest,summary_contract_digest)
    WHERE status='completed';
CREATE UNIQUE INDEX ux_run_events_seq ON run_events(run_id, seq);
CREATE UNIQUE INDEX ux_skill_snapshots_run_rel_path
    ON skill_snapshots(run_id, rel_path)
    WHERE rel_path <> '';
CREATE UNIQUE INDEX ux_standing_approvals_active_scope
    ON standing_approvals(session_id, tool_name, scope_kind, scope_version, scope_key)
    WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX ux_tool_approval_pending
    ON tool_approval_requests(run_id)
    WHERE status = 'pending';
CREATE UNIQUE INDEX ux_tool_calls_run_iteration_index
    ON tool_calls(run_id, iteration, call_index);
CREATE UNIQUE INDEX ux_turns_client_request
    ON turns(session_id, client_request_id);
CREATE TRIGGER agent_runs_commit_format_immutable
BEFORE UPDATE OF commit_format_version ON agent_runs
WHEN NEW.commit_format_version <> OLD.commit_format_version
BEGIN
    SELECT RAISE(ABORT, 'run_commit_format_immutable');
END;
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
CREATE TRIGGER agent_runs_publish_mode_validate_insert
BEFORE INSERT ON agent_runs
WHEN NEW.publish_mode NOT IN ('public_final', 'private_to_parent')
BEGIN
    SELECT RAISE(ABORT, 'run_publish_mode_invalid');
END;
CREATE TRIGGER delegation_attempts_immutable_identity
BEFORE UPDATE OF id,item_id,generation,retry_of_attempt_id,child_run_id,
    authorization_snapshot_json,authorization_snapshot_digest,reserved_budget_json
ON delegation_item_attempts
BEGIN
    SELECT RAISE(ABORT, 'attempt identity and snapshots are immutable');
END;
CREATE TRIGGER delegation_attempts_terminal_frozen
BEFORE UPDATE ON delegation_item_attempts
WHEN OLD.status IN ('succeeded','blocked','needs_input','not_authorized','failed','cancelled','interrupted')
     AND NEW.status <> OLD.status
BEGIN
    SELECT RAISE(ABORT, 'terminal attempts are frozen');
END;
CREATE TRIGGER delegation_continuations_immutable
BEFORE UPDATE ON delegation_attempt_continuations
BEGIN
    SELECT RAISE(ABORT, 'continuation facts are immutable');
END;
CREATE TRIGGER messages_attribution_validate_insert
BEFORE INSERT ON messages
WHEN NEW.speaker_kind NOT IN ('user', 'host', 'role', 'workflow', 'room', 'system')
  OR NEW.visibility NOT IN ('public', 'private', 'room_control', 'legacy_execution')
  OR (NEW.addressee_kind IS NOT NULL AND NEW.addressee_kind NOT IN ('host', 'room', 'role', 'graph', 'workflow'))
BEGIN
    SELECT RAISE(ABORT, 'message_attribution_invalid');
END;
CREATE TRIGGER messages_attribution_validate_update
BEFORE UPDATE OF speaker_kind, visibility, addressee_kind ON messages
WHEN NEW.speaker_kind NOT IN ('user', 'host', 'role', 'workflow', 'room', 'system')
  OR NEW.visibility NOT IN ('public', 'private', 'room_control', 'legacy_execution')
  OR (NEW.addressee_kind IS NOT NULL AND NEW.addressee_kind NOT IN ('host', 'room', 'role', 'graph', 'workflow'))
BEGIN
    SELECT RAISE(ABORT, 'message_attribution_invalid');
END;
CREATE TRIGGER run_messages_validate_insert
BEFORE INSERT ON run_messages
WHEN NEW.ordinal < 0
  OR NEW.role NOT IN ('system', 'user', 'assistant', 'tool')
  OR NEW.visibility NOT IN ('private', 'public')
  OR json_valid(NEW.payload_json) = 0
BEGIN
    SELECT RAISE(ABORT, 'run_message_invalid');
END;
CREATE TRIGGER sessions_mode_immutable
BEFORE UPDATE OF mode ON sessions
WHEN NEW.mode <> OLD.mode
BEGIN
    SELECT RAISE(ABORT, 'session_mode_immutable');
END;
CREATE TRIGGER sessions_mode_validate_insert
BEFORE INSERT ON sessions
WHEN NEW.mode NOT IN ('hosted', 'room')
BEGIN
    SELECT RAISE(ABORT, 'session_mode_invalid');
END;
CREATE TRIGGER sessions_mode_validate_update
BEFORE UPDATE OF mode ON sessions
WHEN NEW.mode NOT IN ('hosted', 'room')
BEGIN
    SELECT RAISE(ABORT, 'session_mode_invalid');
END;
CREATE TRIGGER trg_standing_approvals_active_limit
BEFORE INSERT ON standing_approvals
WHEN NEW.revoked_at IS NULL
 AND (
     SELECT COUNT(*) FROM standing_approvals
     WHERE session_id = NEW.session_id AND revoked_at IS NULL
 ) >= 64
BEGIN
    SELECT RAISE(ABORT, 'standing_approval_limit');
END;

-- session_store_metadata (from 0002): stable per-Session identity.
CREATE TABLE IF NOT EXISTS session_store_metadata (
    singleton       INTEGER PRIMARY KEY CHECK(singleton = 1),
    session_id      TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    schema_version  INTEGER NOT NULL,
    created_at      TEXT NOT NULL
);

-- projection_outbox (from 0002): Session-authority events for the global
-- catalog/usage projections.
CREATE TABLE IF NOT EXISTS projection_outbox (
    event_id        TEXT PRIMARY KEY,
    event_type      TEXT NOT NULL,
    payload_json    TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL,
    projected_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_projection_outbox_pending
ON projection_outbox(created_at, event_id)
WHERE projected_at IS NULL;

-- hook_event_outbox (from 0003): durable webhook delivery queue.
CREATE TABLE IF NOT EXISTS hook_event_outbox (
    delivery_id     TEXT PRIMARY KEY,
    event_id        INTEGER NOT NULL UNIQUE,
    run_id          TEXT NOT NULL,
    session_id      TEXT NOT NULL DEFAULT '',
    event_type      TEXT NOT NULL,
    payload_json    TEXT NOT NULL DEFAULT '{}',
    workspace_id    TEXT NOT NULL DEFAULT '',
    workspace_root  TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL CHECK(status IN ('pending','delivering','delivered','dead')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    last_error      TEXT,
    created_at      TEXT NOT NULL,
    delivered_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_hook_event_outbox_pending
ON hook_event_outbox(status, next_attempt_at, event_id);
