-- Task acceptance conditions are frozen onto the node at Run start, next to
-- budget_json, and their verdict is written back in the same checkpoint CAS.
--
-- Why the specification is a node column and not a read of the frozen version:
-- the fold path reads the version defensively (`if v, err := ...; err == nil`),
-- because budget accounting must survive a version read failure. Hanging
-- acceptance verification off that same read would mean a failed read silently
-- skips verification and records a plain `completed` — the one failure
-- direction this feature exists to remove. A frozen node row is self-describing
-- and survives a version read failure.
--
-- Both columns are NOT NULL with an empty-string default, so "not declared" and
-- "cleared by a reset" are the same value and every reset path can write `''`
-- without a nullable-column branch. Column-level nullable would be worse here:
-- `ResumeFlowRun` resets a failed node by writing an explicit column list, and
-- writing NULL into a NOT NULL column fails at runtime on every resume.
ALTER TABLE run_agent_flow_nodes ADD COLUMN acceptance_json TEXT NOT NULL DEFAULT '';

-- The verdict is the JSON form of agentflow.AcceptanceVerdictValue
-- ({result, checks}); store never interprets it. Empty string means "no verdict
-- recorded": either the task declared no acceptance clause, the task failed
-- before verification, or a reset cleared it (resume, convergence back-edge).
ALTER TABLE run_agent_flow_nodes ADD COLUMN acceptance_verdict TEXT NOT NULL DEFAULT '';
