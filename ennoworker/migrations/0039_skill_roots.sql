-- 0039: Configurable skill source roots.
--
-- Ennote resolves skills from its own default root plus additional roots
-- (pi, claude code, codex, cursor ecosystems) that the user adds in Settings.
-- Lower priority wins on path conflicts; the ennote default root is implicit
-- and not stored here.

CREATE TABLE skill_roots (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL UNIQUE,
    agent_kind TEXT NOT NULL DEFAULT 'generic', -- pi | claude | codex | cursor | generic
    priority   INTEGER NOT NULL DEFAULT 10,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_skill_roots_priority ON skill_roots (priority, enabled);
