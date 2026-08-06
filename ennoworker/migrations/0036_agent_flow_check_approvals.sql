-- 0036: Agent Flow check approvals.
--
-- Deterministic check tasks go through the same policy gate as tools. In Ask
-- mode a check suspends on a durable approval row; the orchestrator waits,
-- then runs or rejects the command according to the user's decision. The row
-- is per (run, task): one pending approval at a time, first decision wins.

CREATE TABLE IF NOT EXISTS run_agent_flow_check_approvals (
    run_id     TEXT NOT NULL REFERENCES run_agent_flow(run_id),
    task_index INTEGER NOT NULL,
    command    TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'approved', 'rejected')),
    decision_client_request_id TEXT,
    requested_at TEXT NOT NULL,
    resolved_at  TEXT,
    PRIMARY KEY (run_id, task_index)
);
