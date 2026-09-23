-- agent_runs.skill_snapshot_digest was never written and never read: the Skill
-- catalog's identity is frozen in the system prompt snapshot instead
-- (skillCatalogState / skillCatalogDigest), where it is bound to the composition
-- that actually used it. A column that no code writes and no code reads is a
-- column that will one day be believed by mistake.
--
-- Dropped rather than left in place so a fresh database and an existing one
-- converge on the same shape: the consolidated initial schema is history and is
-- not edited, so this is an ordinary incremental migration.
ALTER TABLE agent_runs DROP COLUMN skill_snapshot_digest;
