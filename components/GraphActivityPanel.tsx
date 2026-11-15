"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import type { Translator } from "@/lib/messages";
import { nextExpandedRun } from "@/lib/graph-runs";
import { apiFetch } from "@/lib/worker-api.client";
import { ChevronRight, Workflow } from "lucide-react";
import styles from "./GraphActivityPanel.module.css";
import type { components } from "@/lib/worker-api.gen";

type RunAgentFlow = components["schemas"]["RunAgentFlow"];
type RunAgentFlowNode = components["schemas"]["RunAgentFlowNode"];
type AcceptanceVerdict = components["schemas"]["AcceptanceVerdict"];
type AcceptanceCheck = components["schemas"]["AcceptanceCheck"];

const pollIntervalMs = 1500;

function catalogLabel(t: Translator, prefix: string, value: string | undefined): string {
  if (!value) return "";
  const key = `${prefix}.${value}`;
  const text = t(key);
  return text === key ? value : text;
}

/**
 * One check, rendered as `check (code): detail`.
 *
 * The verdict badge is one word on purpose, so the hover text is where the
 * diagnostic lives: `file_empty` alone does not say which file, and a detail
 * alone does not say which leaf kind produced it.
 */
function checkLine(t: Translator, check: AcceptanceCheck): string {
  const label = check.code ? `${check.check} (${check.code})` : check.check;
  return check.detail ? t("graph.acceptance.checkHint", { check: label, detail: check.detail }) : label;
}

/**
 * Hover text for a verdict: what the clause is, and the first leaf that did not
 * hold. A clause is a declared condition on files, never a review of the work,
 * and the title says so — an `unverified` badge otherwise reads as "passed".
 */
function verdictTitle(t: Translator, verdict: AcceptanceVerdict): string {
  const hint = t("graph.acceptance.hint");
  // A verdict row the store could not parse is tolerated there, so the render
  // must tolerate it too instead of throwing inside the panel.
  const checks = verdict.checks ?? [];
  const check = checks.find((entry) => entry.verdict !== "holds") ?? checks[0];
  return check ? `${hint}\n${checkLine(t, check)}` : hint;
}

/**
 * GraphActivityPanel is the right-panel "Graphs" channel: it lists the Agent
 * Flow runs of the current session, newest first. Each run is a collapsible
 * card; the most recent run is expanded by default. Running runs poll while
 * active so task progress stays live without an SSE subscription.
 */
export function GraphActivityPanel({ sessionId, onCount }: {
  sessionId: string | null;
  /** How many runs this session has, reported so the tab can carry the number. */
  onCount?: (count: number) => void;
}) {
  const t = useT();
  const [runs, setRuns] = useState<RunAgentFlow[]>([]);
  const [nodes, setNodes] = useState<Record<string, RunAgentFlowNode[]>>({});
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  /**
   * The run the reader closed, and the count callback, both by ref.
   *
   * A render must not restart the poll: the parent passes an inline callback, so a
   * dependency on it meant every parent render refetched the list — and that refetch
   * is what re-expanded a run the reader had just collapsed.
   */
  const dismissed = useRef<string | null>(null);
  const onCountRef = useRef(onCount);
  useEffect(() => { onCountRef.current = onCount; }, [onCount]);

  const load = useCallback(async () => {
    if (!sessionId) return;
    try {
      const runs = await apiFetch<RunAgentFlow[]>(`/v1/sessions/${encodeURIComponent(sessionId)}/graph-runs`);
      const filtered = runs ?? [];
      setRuns(filtered);
      onCountRef.current?.(filtered.length);
      setError(null);
      setExpanded((current) => nextExpandedRun(current, filtered, dismissed.current));
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

  // Load task checkpoints for the expanded run, and keep them as fresh as the
  // list: the card's unverified marker is recomputed every poll, so a frozen
  // node list would show a marker with no badge next to the task that caused it.
  useEffect(() => {
    if (!sessionId || !expanded) return;
    const controller = new AbortController();
    let cancelled = false;
    let timer: number | undefined;
    const loadDetail = async () => {
      try {
        const detail = await apiFetch<{ run: RunAgentFlow; nodes: RunAgentFlowNode[] }>(
          `/v1/sessions/${encodeURIComponent(sessionId)}/graph-runs/${encodeURIComponent(expanded)}`,
        );
        if (cancelled) return;
        setNodes((previous) => ({ ...previous, [expanded]: detail.nodes ?? [] }));
      } catch {
        if (!cancelled) setError("graph.loadFailed");
      }
      if (cancelled) return;
      timer = window.setTimeout(loadDetail, pollIntervalMs);
    };
    void loadDetail();
    return () => {
      cancelled = true;
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
    };
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
            {t("graph.running", { count: String(runningCount) })}
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
              onToggle={() => setExpanded((current) => {
                if (current === run.runId) {
                  // Remember the choice so the next refresh does not undo it.
                  dismissed.current = run.runId ?? null;
                  return null;
                }
                dismissed.current = null;
                return run.runId ?? null;
              })}
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
  t: Translator;
}) {
  const state = run.state;
  const nodeCount = nodes.length;
  const done = nodes.filter((node) => node.terminalState === "completed").length;
  const flowLabel = catalogLabel(t, "graph.flow", state);
  // `doesNotHold` already fails the Run, so the state dot carries it. An
  // `unverified` clause leaves a completed Run looking clean, which is the one
  // signal the card has to add itself.
  const unverified = run.acceptance?.unverified ?? 0;

  return (
    <div className={styles.card} data-testid="graph-run-card" data-run-id={run.runId}>
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
          {nodeCount > 0 ? t("graph.tasks", { done: String(done), total: String(nodeCount) }) : flowLabel}
        </span>
        {unverified > 0 && (
          <span
            className={styles.unverifiedMarker}
            data-testid="graph-acceptance-unverified"
            title={t("graph.acceptance.hint")}
          >
            {t("graph.acceptance.unverifiedCount", { count: String(unverified) })}
          </span>
        )}
      </button>

      {expanded && (
        <div className={styles.details}>
          <div className={styles.meta}>
            <span>{t("graph.stateLabel", { state: flowLabel })}</span>
            {run.totalTokensUsed ? <span>{t("graph.tokens", { count: String(run.totalTokensUsed) })}</span> : null}
            {run.createdAt ? <span>{formatTime(run.createdAt, t)}</span> : null}
          </div>
          {nodesLoading ? (
            <div className={styles.stateLine}>{t("graph.loadingTasks")}</div>
          ) : nodes.length === 0 ? (
            <div className={styles.stateLine}>{t("graph.noTasks")}</div>
          ) : (
            <ul className={styles.nodeList}>
              {nodes.map((node) => {
                const verdict = node.acceptanceVerdict;
                return (
                  <li key={node.taskIndex ?? node.handle} className={styles.nodeRow}>
                    <span className={styles.nodeDot} data-node-state={node.terminalState} />
                    <span className={styles.nodeName}>
                      {node.handle}
                    </span>
                    {verdict && (
                      <span
                        className={styles.verdict}
                        data-acceptance-verdict={verdict.result}
                        data-testid="graph-acceptance-badge"
                        title={verdictTitle(t, verdict)}
                      >
                        {t(`graph.acceptance.${verdict.result}`)}
                      </span>
                    )}
                    {verdict?.result === "does_not_hold" && node.errorCode && (
                      // The child Run finished; what failed is the clause on its
                      // artifacts. Without the code, a failed row reads as "the
                      // work failed" rather than "the work ran and missed".
                      <span className={styles.nodeErrorCode} title={t("graph.acceptance.errorCodeHint")}>
                        {node.errorCode}
                      </span>
                    )}
                    <span className={styles.nodeState}>
                      {catalogLabel(t, "graph.node", node.terminalState)}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

function formatTime(value: string, t: Translator): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const now = new Date();
  const minutes = Math.floor((now.getTime() - date.getTime()) / 60000);
  if (minutes < 1) return t("graph.justNow");
  if (minutes < 60) return t("graph.minutesAgo", { count: String(minutes) });
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}
