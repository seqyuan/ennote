"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import { ChevronRight, Workflow } from "lucide-react";
import styles from "./GraphActivityPanel.module.css";
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
    <div className={styles.root}>
      <div className={styles.header}>
        <span className={styles.title}>
          <Workflow size={13} aria-hidden="true" />
          Graph runs
        </span>
        {running && (
          <span className={styles.running}>
            <span className={styles.pulseDot} />
            running
          </span>
        )}
      </div>

      {error && <div className={styles.error}>{error}</div>}

      <div className={styles.scroll}>
        {!sessionId ? (
          <div className={styles.empty}>
            Select a session to see its graph runs.
          </div>
        ) : runs.length === 0 ? (
          <div className={styles.empty}>
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
    <div className={styles.card}>
      <button
        type="button"
        onClick={onToggle}
        className={styles.cardButton}
        data-expanded={expanded || undefined}
        aria-expanded={expanded}
      >
        <ChevronRight size={11} className={styles.chevron} data-expanded={expanded || undefined} aria-hidden="true" />
        <span className={`${styles.stateDot} ${styles.flowDot}`} data-flow-state={state} />
        <span className={styles.cardTitle}>
          {run.inputs?.name ? String(run.inputs.name) : "Graph run"}
        </span>
        <span className={styles.cardSummary}>
          {nodeCount > 0 ? `${done}/${nodeCount} tasks` : flowStateLabel[state ?? ""] ?? state}
        </span>
      </button>

      {expanded && (
        <div className={styles.details}>
          <div className={styles.meta}>
            <span>state: {flowStateLabel[state ?? ""] ?? state}</span>
            {run.totalTokensUsed ? <span>tokens: {run.totalTokensUsed}</span> : null}
            {run.createdAt ? <span>{formatTime(run.createdAt)}</span> : null}
          </div>
          {nodesLoading ? (
            <div className={styles.stateLine}>Loading tasks…</div>
          ) : nodes.length === 0 ? (
            <div className={styles.stateLine}>No task checkpoints.</div>
          ) : (
            <ul className={styles.nodeList}>
              {nodes.map((node) => (
                <li key={node.taskIndex ?? node.handle} className={styles.nodeRow}>
                  <span className={styles.nodeDot} data-node-state={node.terminalState} />
                  <span className={styles.nodeName}>
                    {node.handle}
                  </span>
                  <span className={styles.nodeState}>
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
