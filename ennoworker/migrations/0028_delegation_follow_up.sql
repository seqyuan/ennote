-- Item 6 Stage 4: exact private child continuation.
-- needs_input and follow-up resume one exact persisted child attempt. The
-- continuation edge records the source attempt, kind, and bounded input with
-- a canonical digest; both attempts belong to the same logical item and the
-- exact frozen Role version (enforced by application validation and the
-- CHECK below).

CREATE TABLE delegation_attempt_continuations (
    attempt_id        TEXT PRIMARY KEY REFERENCES delegation_item_attempts(id),
    source_attempt_id TEXT NOT NULL REFERENCES delegation_item_attempts(id),
    kind              TEXT NOT NULL CHECK(kind IN ('input','follow_up')),
    input_json        TEXT NOT NULL,
    input_digest      TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    CHECK(attempt_id <> source_attempt_id)
);

CREATE TRIGGER delegation_continuations_immutable
BEFORE UPDATE ON delegation_attempt_continuations
BEGIN
    SELECT RAISE(ABORT, 'continuation facts are immutable');
END;
