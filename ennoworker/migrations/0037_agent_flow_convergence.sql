-- 0037: Agent Flow convergence round counters.
--
-- Each declared convergence rule {from, to, max_rounds} gets an independent
-- durable round counter so a crash between back-edges never loses count
-- (v2 §5.2 / §7.5: resume identity + checkpoint continuation must not reset
-- the loop budget). JSON map keyed by "from\x00to".

ALTER TABLE run_agent_flow ADD COLUMN convergence_rounds_json TEXT NOT NULL DEFAULT '{}';
