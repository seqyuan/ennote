-- Pinning an artifact to the Project canvas (canvas design §3.3) is a state of
-- the artifact row, so it is a column and not a table.
--
-- Why not a `pinned_artifacts` table: the artifact rows live in the Session's own
-- SQLite file, and this column is state *of* one of those rows. A separate table
-- would be a second place that answers "is this artifact pinned?", so every read
-- would need a join to agree with it, and a delete of an artifact would have to
-- remember to clean up a row nothing points at. `pinned_at IS NOT NULL` is the
-- whole predicate.
--
-- The Project-level canvas (§3.3: a Worker resource, not a client preference) does
-- not contradict that. It spans Session databases that no single SQLite file can
-- join, so the merge is the API's job: ask each Session for its own pinned rows
-- and merge by `pinned_at`. A project-level pin table held inside one Session's
-- database would be worse than either option -- a second authority that the other
-- Sessions of the same project cannot see.
--
-- `pinned_at` is TEXT written in a fixed-width RFC 3339 layout
-- (`2006-01-02T15:04:05.000000000Z`, see pinnedAtLayout in internal/artifacts).
-- SQLite has no datetime type, so ORDER BY compares strings: RFC3339Nano trims
-- trailing zeros and would order "12:00:00.1Z" after "12:00:00.12Z". A fixed width
-- makes the collision-free string order the chronological one.
ALTER TABLE artifacts ADD COLUMN pinned_at TEXT;

-- Inside one Session the read is "everything pinned, newest first"; the index
-- keeps that from becoming a scan of every artifact the Session ever published.
-- It is a partial index: the query already filters `pinned_at IS NOT NULL`, so the
-- planner picks this one anyway, and the file grows with the number of pins rather
-- than the number of artifacts. (The second sort key, id, still needs a temp
-- B-tree; with a handful of pins that is nothing.)
CREATE INDEX IF NOT EXISTS idx_artifacts_pinned_at ON artifacts(pinned_at)
    WHERE pinned_at IS NOT NULL;
