ALTER TABLE skill_snapshots ADD COLUMN rel_path TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ux_skill_snapshots_run_rel_path
    ON skill_snapshots(run_id, rel_path)
    WHERE rel_path <> '';
