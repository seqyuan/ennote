-- 0038: Agent Flow fan_out child sets.
--
-- A fan_out task expands into N read-only parallel child Runs; the node
-- records every child id so recovery, cancellation, and budget accounting
-- can address the whole set (the single child_run_id column stays for
-- ordinary tasks).

ALTER TABLE run_agent_flow_nodes ADD COLUMN child_run_ids_json TEXT NOT NULL DEFAULT '[]';
