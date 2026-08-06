-- 0035: Agent Flow cancel requests.
--
-- Flow cancellation is durable: the API marks cancel_requested, the
-- orchestrator poll sees it, hard-cancels the active child Run, and
-- terminalizes the meta-Run as cancelled. Future tasks are never scheduled.

ALTER TABLE run_agent_flow ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0;
