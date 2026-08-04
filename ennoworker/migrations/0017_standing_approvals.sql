-- Standing scoped approvals: user-granted per-session persistent rules
-- that auto-approve future tool calls matching a canonical scope.
--
-- Rules are session-scoped, soft-revoked, and bound to a specific tool's
-- versioned scope kind+key. A BEFORE INSERT trigger enforces the per-session
-- 64-active-rule limit at the database level, independent of connection count.

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

CREATE UNIQUE INDEX ux_standing_approvals_active_scope
    ON standing_approvals(session_id, tool_name, scope_kind, scope_version, scope_key)
    WHERE revoked_at IS NULL;

CREATE INDEX ix_standing_approvals_session_active
    ON standing_approvals(session_id, created_at DESC)
    WHERE revoked_at IS NULL;

-- Per-session active rule cap.  New rows for scopes that already have an active
-- rule skip the count check; new unique scopes are capped at 64.
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

-- Server-authoritative candidates persisted at suspension time so that the
-- Decide transaction has a trusted source of truth independent of the client.
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

-- One row per created standing rule, mapping an approval decision back to the
-- rule it created. Also provides idempotency: same decision + same call-index
-- set resolves to the same grants.
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

-- Track which standing rule authorised a tool execution.  Empty string means
-- not covered by a standing rule.
ALTER TABLE tool_calls
    ADD COLUMN standing_rule_id TEXT NOT NULL DEFAULT '';
