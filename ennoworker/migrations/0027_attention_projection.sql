-- Item 6 Stage 3: cross-Session Attention projection.
-- Attention is a reconstructible projection of authoritative execution facts
-- (approvals, delegation input, completions, failures). It is never a second
-- authorization or execution state machine: source decisions stay
-- first-writer-wins and write back to the original durable rows.

CREATE TABLE attention_items (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL REFERENCES projects(id),
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

CREATE INDEX ix_attention_pending ON attention_items(status, project_id, created_at);
CREATE INDEX ix_attention_session ON attention_items(session_id, status, created_at);
