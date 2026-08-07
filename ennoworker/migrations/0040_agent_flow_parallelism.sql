-- Agent Flow ready-set parallel dispatch (2026-08-07):
-- freeze each task node's concurrency class (reader/writer) and its declared
-- writes scope at run start so the orchestrator can dispatch independent
-- tasks concurrently without re-resolving Role definitions at runtime.
ALTER TABLE run_agent_flow_nodes ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0;
ALTER TABLE run_agent_flow_nodes ADD COLUMN writes_json TEXT NOT NULL DEFAULT '[]';
