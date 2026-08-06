"use client";

import { AlertTriangle, Loader2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { AgentFlowCheckApproval } from "@/components/settings/types";

// FlowCheckApprovalStrip sits above the Composer and surfaces pending check
// task approvals for the current session's Agent Flow runs, without blocking
// input. Decisions are the same durable endpoint the Flows settings tab uses.
export function FlowCheckApprovalStrip({ projectId, sessionId }: {
  projectId: string | null;
  sessionId: string | undefined;
}) {
  const [approvals, setApprovals] = useState<AgentFlowCheckApproval[]>([]);
  const [refreshing, setRefreshing] = useState(false);
  const [deciding, setDeciding] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!projectId || !sessionId) {
      setApprovals([]);
      return;
    }
    setRefreshing(true);
    try {
      const all = await apiFetch<AgentFlowCheckApproval[]>(
        `/v1/projects/${encodeURIComponent(projectId)}/agent-flows/check-approvals`,
      );
      setApprovals((all ?? []).filter((item) => item.sessionId === sessionId));    } catch {
      // Silent: the strip is a convenience projection; failures surface in the settings tab.
    } finally {
      setRefreshing(false);
    }
  }, [projectId, sessionId]);

  useEffect(() => {
    const t0 = window.setTimeout(() => void refresh(), 0);
    const timer = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, 4000);
    return () => {
      window.clearTimeout(t0);
      window.clearInterval(timer);
    };
  }, [refresh]);

  const decide = useCallback(async (approval: AgentFlowCheckApproval, approved: boolean) => {
    if (!projectId || !approval.runId || approval.taskIndex === undefined) return;
    const runId = approval.runId;
    const taskIndex = approval.taskIndex;
    const key = `${runId}:${taskIndex}`;
    setDeciding(key);
    try {
      await apiFetch(
        `/v1/projects/${encodeURIComponent(projectId)}/agent-flows/check-approvals/${encodeURIComponent(runId)}/${taskIndex}/decide`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ approved, clientRequestId: `check-${approval.runId}-${approval.taskIndex}` }),
        },
      );
      setApprovals((current) => current.filter((item) => !(item.runId === runId && item.taskIndex === taskIndex)));
    } catch {
      // Keep the item visible on failure; the user can retry.
    } finally {
      setDeciding(null);
    }
  }, [projectId]);

  if (!approvals.length) return null;

  return <div className="background-delegation-strip flow-check-strip" role="status" aria-label="Flow check approval">
    <div className="background-delegation-header">
      <span><AlertTriangle size={13} aria-hidden="true" /> Flow check approval</span>
      <span>{approvals.length} pending</span>
      {refreshing && <Loader2 size={12} aria-hidden="true" className="delivery-spinner" />}
    </div>
    <div className="delivery-list" role="list">
      {approvals.map((approval) => {
        const runId = approval.runId ?? "";
        const key = `${runId}:${approval.taskIndex}`;
        const busy = deciding === key;
        return (
          <div className="delivery-row" key={key} role="listitem">
            <span className="delivery-icon"><AlertTriangle size={13} aria-hidden="true" /></span>
            <span className="delivery-identity">
              <strong>task {approval.taskIndex} · {runId.slice(0, 8) || "—"}</strong>
              <span className="flow-check-command" title={approval.command}>{approval.command}</span>
            </span>
            <span className="flow-check-actions">
              <button
                type="button"
                disabled={Boolean(busy)}
                onClick={() => decide(approval, true)}
                className="secondary-btn flow-check-approve"
              >
                Approve
              </button>
              <button
                type="button"
                disabled={Boolean(busy)}
                onClick={() => decide(approval, false)}
                className="secondary-btn flow-check-reject"
              >
                Reject
              </button>
            </span>
          </div>
        );
      })}
    </div>
  </div>;
}
