"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type RunAgentFlow = components["schemas"]["RunAgentFlow"];
type RunAgentFlowNode = components["schemas"]["RunAgentFlowNode"];

const pollIntervalMs = 1500;

const nodeStateLabel: Record<string, string> = {
  pending: "Pending",
  running: "Running",
  completed: "Done",
  failed: "Failed",
  blocked: "Blocked",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
};

const flowStateLabel: Record<string, string> = {
  pending: "Pending",
  running: "Running",
  completed: "Completed",
  failed: "Failed",
  cancelled: "Cancelled",
  convergence_exceeded: "Convergence exceeded",
  budget_exceeded: "Budget exceeded",
};

function flowStateColor(state?: string): string {
  switch (state) {
    case "running": return "#2563EB";
    case "completed": return "#16A34A";
    case "failed": return "#DC2626";
    case "cancelled": case "convergence_exceeded": case "budget_exceeded": return "#D97706";
    default: return "var(--text-dim)";
  }
}

function nodeStateColor(state?: string): string {
  switch (state) {
    case "running": return "#2563EB";
    case "completed": return "#16A34A";
    case "failed": return "#DC2626";
    case "blocked": return "#D97706";
    case "cancelled": case "interrupted": return "var(--text-dim)";
    default: return "var(--text-dim)";
  }
}

/**
 * GraphActivityPanel is the right-panel "Graphs" channel: it lists the Agent
 * Flow runs of the current session, newest first. Each run is a collapsible
 * card; the most recent run is expanded by default. Running runs poll while
 * active so task progress stays live without an SSE subscription.
 */
export function GraphActivityPanel({ sessionId }: {
  sessionId: string | null;
}) {
  const [runs, setRuns] = useState<RunAgentFlow[]>([]);
  const [nodes, setNodes] = useState<Record<string, RunAgentFlowNode[]>>({});
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!sessionId) return;
    try {
      const runs = await apiFetch<RunAgentFlow[]>(`/v1/sessions/${encodeURIComponent(sessionId)}/graph-runs`);
      const filtered = runs ?? [];
      setRuns(filtered);
      setError(null);
      // Expand the newest run when the list changes (empty -> non-empty or a
      // newer run appears).
      setExpanded((current) => {
        if (filtered.length === 0) return null;
        const newest = filtered[0].runId ?? null;
        if (newest === null) return null;
        if (current === null || !filtered.some((run) => run.runId === current)) return newest;
        const newestCreated = filtered[0].createdAt;
        const currentCreated = filtered.find((run) => run.runId === current)?.createdAt;
        if (newestCreated && currentCreated && currentCreated < newestCreated) return newest;
        return current;
      });
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [sessionId]);

  useEffect(() => {
    const controller = new AbortController();
    let timer: number | undefined;
    let cancelled = false;
    const poll = async () => {
      await load();
      if (cancelled) return;
      timer = window.setTimeout(poll, pollIntervalMs);
    };
    void poll();
    return () => {
      cancelled = true;
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [load]);

  // Load task checkpoints for the expanded run.
  useEffect(() => {
    if (!sessionId || !expanded) return;
    let cancelled = false;
    void apiFetch<{ run: RunAgentFlow; nodes: RunAgentFlowNode[] }>(
      `/v1/sessions/${encodeURIComponent(sessionId)}/graph-runs/${encodeURIComponent(expanded)}`,
    ).then((detail) => {
      if (cancelled) return;
      setNodes((previous) => ({ ...previous, [expanded]: detail.nodes ?? [] }));
    }).catch(() => { if (!cancelled) setError("Failed to load graph tasks"); });
    return () => { cancelled = true; };
  }, [expanded, sessionId]);

  const running = useMemo(() => runs.some((run) => run.state === "running"), [runs]);

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <div style={{ padding: "10px 14px 6px", display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <span style={{ fontSize: 12, fontWeight: 600, color: "var(--text)", display: "flex", alignItems: "center", gap: 6 }}>
          <GraphIcon />
          {sessionId ? "Graph runs" : "Graph runs"}
        </span>
        {running && (
          <span style={{ fontSize: 10, color: "#2563EB", display: "flex", alignItems: "center", gap: 4 }}>
            <span style={{ width: 6, height: 6, borderRadius: "50%", background: "#2563EB", animation: "pulse 2s infinite" }} />
            running
          </span>
        )}
      </div>

      {error && <div style={{ padding: "8px 14px", fontSize: 11, color: "#DC2626" }}>{error}</div>}

      <div style={{ flex: 1, minHeight: 0, overflowY: "auto", padding: "0 8px 12px" }}>
        {!sessionId ? (
          <div style={{ padding: 16, color: "var(--text-dim)", fontSize: 12, textAlign: "center" }}>
            Select a session to see its graph runs.
          </div>
        ) : runs.length === 0 ? (
          <div style={{ padding: 16, color: "var(--text-dim)", fontSize: 12, textAlign: "center" }}>
            No graph runs in this session yet.
          </div>
        ) : (
          runs.map((run) => (
            <RunCard
              key={run.runId}
              run={run}
              nodes={nodes[run.runId ?? ""] ?? []}
              expanded={expanded === run.runId}
              onToggle={() => setExpanded((current) => current === run.runId ? null : (run.runId ?? null))}
              nodesLoading={expanded === run.runId && nodes[run.runId ?? ""] === undefined}
            />
          ))
        )}
      </div>
    </div>
  );
}

function RunCard({ run, nodes, expanded, onToggle, nodesLoading }: {
  run: RunAgentFlow;
  nodes: RunAgentFlowNode[];
  expanded: boolean;
  onToggle: () => void;
  nodesLoading: boolean;
}) {
  const state = run.state;
  const nodeCount = nodes.length;
  const done = nodes.filter((node) => node.terminalState === "completed").length;

  return (
    <div style={{ border: "1px solid var(--border)", borderRadius: 8, marginBottom: 8, overflow: "hidden" }}>
      <button
        type="button"
        onClick={onToggle}
        style={{
          display: "flex", alignItems: "center", gap: 8,
          width: "100%", padding: "8px 10px",
          background: expanded ? "var(--bg-selected)" : "transparent",
          border: "none", cursor: "pointer", color: "var(--text)", textAlign: "left", fontSize: 12,
        }}
        aria-expanded={expanded}
      >
        <svg width="11" height="11" viewBox="0 0 10 10" fill="none" stroke="var(--text-dim)" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, transform: expanded ? "rotate(90deg)" : "none", transition: "transform 0.15s" }}>
          <polyline points="2 3.5 5 6.5 8 3.5" />
        </svg>
        <span style={{ width: 8, height: 8, borderRadius: "50%", background: flowStateColor(state), flexShrink: 0 }} />
        <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {run.inputs?.name ? String(run.inputs.name) : "Graph run"}
        </span>
        <span style={{ flexShrink: 0, fontSize: 10, color: "var(--text-dim)" }}>
          {nodeCount > 0 ? `${done}/${nodeCount} tasks` : flowStateLabel[state ?? ""] ?? state}
        </span>
      </button>

      {expanded && (
        <div style={{ borderTop: "1px solid var(--border)", padding: "6px 10px 10px 22px" }}>
          <div style={{ fontSize: 10, color: "var(--text-dim)", marginBottom: 6, display: "flex", gap: 12, flexWrap: "wrap" }}>
            <span>state: {flowStateLabel[state ?? ""] ?? state}</span>
            {run.totalTokensUsed ? <span>tokens: {run.totalTokensUsed}</span> : null}
            {run.createdAt ? <span>{formatTime(run.createdAt)}</span> : null}
          </div>
          {nodesLoading ? (
            <div style={{ fontSize: 11, color: "var(--text-dim)", padding: "4px 0" }}>Loading tasks…</div>
          ) : nodes.length === 0 ? (
            <div style={{ fontSize: 11, color: "var(--text-dim)", padding: "4px 0" }}>No task checkpoints.</div>
          ) : (
            <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
              {nodes.map((node) => (
                <li key={node.taskIndex ?? node.handle} style={{ display: "flex", alignItems: "center", gap: 7, padding: "3px 0", fontSize: 11 }}>
                  <span style={{ width: 7, height: 7, borderRadius: "50%", background: nodeStateColor(node.terminalState), flexShrink: 0 }} />
                  <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", color: "var(--text)" }}>
                    {node.handle}
                  </span>
                  <span style={{ flexShrink: 0, color: "var(--text-dim)", fontSize: 10 }}>
                    {nodeStateLabel[node.terminalState ?? ""] ?? node.terminalState}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

function formatTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const now = new Date();
  const minutes = Math.floor((now.getTime() - date.getTime()) / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function GraphIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="12" cy="5" r="2" />
      <circle cx="5" cy="19" r="2" />
      <circle cx="19" cy="19" r="2" />
      <path d="M12 7v4M5 17v-2h6v4M19 17v-2h-6" />
    </svg>
  );
}
