-- 0031: MCP tools-only client substrate (roadmap item 11).
--
-- Three-layer model: immutable Server Profile versions describe how to
-- connect; Project Bindings describe desired enablement and the exact
-- selected tools; Run snapshots freeze each Run's precise capability set.
-- The catalog cache is scoped to binding revision + auth generation so a
-- server that returns a different toolset per identity can never leak a
-- cross-Project/credential catalog. Credential values never appear here;
-- only `env:` / `file:` / `keyring:` reference names are stored.
CREATE TABLE IF NOT EXISTS mcp_server_profiles (
    id            TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL,
    slug          TEXT NOT NULL,
    source_kind   TEXT NOT NULL CHECK (source_kind IN ('managed', 'project_file', 'bundled')),
    project_scope TEXT,               -- NULL for global (managed/bundled), else project_file owner project
    source_locator TEXT,              -- .ennote/mcp.json path or bundled descriptor key
    lifecycle_status TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle_status IN ('active', 'archived')),
    latest_version INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    UNIQUE(slug, source_kind, project_scope)
);

CREATE TABLE IF NOT EXISTS mcp_server_profile_versions (
    id            TEXT PRIMARY KEY,
    profile_id    TEXT NOT NULL REFERENCES mcp_server_profiles(id),
    version       INTEGER NOT NULL,
    transport     TEXT NOT NULL CHECK (transport IN ('stdio', 'streamable_http', 'legacy_sse')),
    executable    TEXT,
    argv_json     TEXT,               -- structured argv; never a shell string
    endpoint      TEXT,               -- streamable_http/legacy_sse URL
    env_literals_json  TEXT NOT NULL DEFAULT '{}',
    env_credentials_json TEXT NOT NULL DEFAULT '{}',  -- {envName: credentialRef}
    header_literals_json  TEXT NOT NULL DEFAULT '{}',
    header_credentials_json TEXT NOT NULL DEFAULT '{}',
    cwd           TEXT,
    timeout_ms    INTEGER NOT NULL DEFAULT 15000,
    network_policy TEXT NOT NULL DEFAULT 'default' CHECK (network_policy IN ('default', 'loopback', 'private', 'custom_ca', 'insecure')),
    config_digest TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    UNIQUE(profile_id, version)
);

CREATE TABLE IF NOT EXISTS project_mcp_bindings (
    id                   TEXT PRIMARY KEY,
    project_id           TEXT NOT NULL,
    profile_version_id   TEXT NOT NULL REFERENCES mcp_server_profile_versions(id),
    desired_enabled      INTEGER NOT NULL DEFAULT 0,
    required             INTEGER NOT NULL DEFAULT 1,   -- required servers block Run start when unavailable
    selected_remote_tool_names_json TEXT NOT NULL DEFAULT '[]',
    credential_refs_json TEXT NOT NULL DEFAULT '{}',   -- {envName: credentialRef}; refs only
    revision             INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    UNIQUE(project_id, profile_version_id)
);

CREATE TABLE IF NOT EXISTS mcp_catalog_cache (
    binding_id             TEXT NOT NULL REFERENCES project_mcp_bindings(id),
    binding_revision       INTEGER NOT NULL,
    profile_version_id     TEXT NOT NULL REFERENCES mcp_server_profile_versions(id),
    protocol_version       TEXT NOT NULL,
    auth_generation        INTEGER NOT NULL DEFAULT 0,
    credential_digest      TEXT NOT NULL DEFAULT '',   -- digest of credential REFERENCE names only; never values
    server_identity_digest TEXT NOT NULL,
    catalog_digest         TEXT NOT NULL,
    tools_json             TEXT NOT NULL,   -- normalized tool definitions (bounded)
    annotations_json       TEXT NOT NULL DEFAULT '{}',
    fetched_at             TEXT NOT NULL,
    stale_at               TEXT,
    PRIMARY KEY (binding_id, binding_revision, profile_version_id, protocol_version, auth_generation, credential_digest)
);

CREATE TABLE IF NOT EXISTS run_mcp_servers (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL,
    binding_id          TEXT NOT NULL,
    binding_revision    INTEGER NOT NULL,
    profile_version_id  TEXT NOT NULL REFERENCES mcp_server_profile_versions(id),
    config_digest       TEXT NOT NULL,
    negotiated_protocol TEXT NOT NULL,
    server_identity_digest TEXT NOT NULL,
    catalog_digest      TEXT NOT NULL,
    required            INTEGER NOT NULL,
    unavailable_reason  TEXT,
    connection_generation INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS run_mcp_tools (
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

CREATE TABLE IF NOT EXISTS mcp_requests (
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

CREATE INDEX IF NOT EXISTS idx_run_mcp_servers_run ON run_mcp_servers(run_id);
CREATE INDEX IF NOT EXISTS idx_run_mcp_tools_server ON run_mcp_tools(run_server_id);
CREATE INDEX IF NOT EXISTS idx_mcp_requests_run ON mcp_requests(run_id);
CREATE INDEX IF NOT EXISTS idx_project_mcp_bindings_project ON project_mcp_bindings(project_id);
