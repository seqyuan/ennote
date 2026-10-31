"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { apiFetch } from "@/lib/worker-api.client";
import { ChevronRight, Workflow } from "lucide-react";
import styles from "./GraphActivityPanel.module.css";
import type { components } from "@/lib/worker-api.gen";

type RunAgentFlow = components["schemas"]["RunAgentFlow"];
type RunAgentFlowNode = components["schemas"]["RunAgentFlowNode"];

const pollIntervalMs = 1500;

function catalogLabel(t: (key: string) => string, prefix: string, value: string | undefined): string {
  if (!value) return "";
  const key = `${prefix}.${value}`;
  const text = t(key);
  return text === key ? value : text;
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
  const t = useT();
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
    }).catch(() => { if (!cancelled) setError("graph.loadFailed"); });
    return () => { cancelled = true; };
  }, [expanded, sessionId]);

  const runningCount = useMemo(() => runs.filter((run) => run.state === "running").length, [runs]);

  return (
    <div className={styles.root}>
      <div className={styles.header}>
        <span className={styles.title}>
          <Workflow size={13} aria-hidden="true" />
          {t("graph.title")}
        </span>
        {runningCount > 0 && (
          <span className={styles.running}>
            {t("graph.running").replace("{count}", String(runningCount))}
          </span>
        )}
      </div>

      {error && <div className={styles.error}>{error === "graph.loadFailed" ? t(error) : error}</div>}

      <div className={styles.scroll}>
        {!sessionId ? (
          <div className={styles.empty}>
            {t("graph.emptySession")}
          </div>
        ) : runs.length === 0 ? (
          <div className={styles.empty}>
            {t("graph.emptyRuns")}
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
              t={t}
            />
          ))
        )}
      </div>
    </div>
  );
}

function RunCard({ run, nodes, expanded, onToggle, nodesLoading, t }: {
  run: RunAgentFlow;
  nodes: RunAgentFlowNode[];
  expanded: boolean;
  onToggle: () => void;
  nodesLoading: boolean;
  t: (key: string) => string;
}) {
  const state = run.state;
  const nodeCount = nodes.length;
  const done = nodes.filter((node) => node.terminalState === "completed").length;
  const flowLabel = catalogLabel(t, "graph.flow", state);

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
          {run.inputs?.name ? String(run.inputs.name) : t("graph.unnamed")}
        </span>
        <span className={styles.cardSummary}>
          {nodeCount > 0 ? t("graph.tasks").replace("{done}", String(done)).replace("{total}", String(nodeCount)) : flowLabel}
        </span>
      </button>

      {expanded && (
        <div className={styles.details}>
          <div className={styles.meta}>
            <span>{t("graph.stateLabel").replace("{state}", flowLabel)}</span>
            {run.totalTokensUsed ? <span>{t("graph.tokens").replace("{count}", String(run.totalTokensUsed))}</span> : null}
            {run.createdAt ? <span>{formatTime(run.createdAt, t)}</span> : null}
          </div>
          {nodesLoading ? (
            <div className={styles.stateLine}>{t("graph.loadingTasks")}</div>
          ) : nodes.length === 0 ? (
            <div className={styles.stateLine}>{t("graph.noTasks")}</div>
          ) : (
            <ul className={styles.nodeList}>
              {nodes.map((node) => (
                <li key={node.taskIndex ?? node.handle} className={styles.nodeRow}>
                  <span className={styles.nodeDot} data-node-state={node.terminalState} />
                  <span className={styles.nodeName}>
                    {node.handle}
                  </span>
                  <span className={styles.nodeState}>
                    {catalogLabel(t, "graph.node", node.terminalState)}
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

function formatTime(value: string, t: (key: string) => string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const now = new Date();
  const minutes = Math.floor((now.getTime() - date.getTime()) / 60000);
  if (minutes < 1) return t("graph.justNow");
  if (minutes < 60) return t("graph.minutesAgo").replace("{count}", String(minutes));
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}
