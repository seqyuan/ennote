"use client";

import { useEffect, useRef, useState } from "react";
import { CircleAlert, CircleCheck, LoaderCircle, PauseCircle, RotateCw } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import { formatTokenCount } from "@/lib/message-chrome";
import { decodeRunStatus, runStatusText } from "@/lib/run-status-copy";

export interface RunStatusProps {
  status: string;
  active: boolean;
  waiting: boolean;
  waitingForQuestion?: boolean;
  reconnecting: boolean;
  compacting: boolean;
  /** Server clock for this run. startedAt, or createdAt when the run has not started. */
  startedAt?: string | null;
  runId?: string | null;
  cacheReadTokens?: number;
  inputTokens?: number;
  outputTokens?: number;
}

export function RunStatus({
  status, active, waiting, waitingForQuestion, reconnecting, compacting,
  startedAt, runId, cacheReadTokens = 0, inputTokens = 0, outputTokens = 0,
}: RunStatusProps) {
  const t = useT();
  const elapsed = useElapsed(active, runId, startedAt);
  if (!active && !status) return null;
  const code = decodeRunStatus(status).code;
  const label = reconnecting
    ? t("run.status.reconnecting")
    : waiting
      ? t(waitingForQuestion ? "run.status.waitingAnswer" : "run.status.waitingApproval")
      : status
        ? runStatusText(status, t)
        : compacting ? t("run.status.compacting") : t("run.status.running");
  const failed = code === "interrupted" || code === "run_compaction_skipped";
  const Icon = reconnecting ? RotateCw : waiting ? PauseCircle : active ? LoaderCircle : failed ? CircleAlert : CircleCheck;
  const showMetrics = cacheReadTokens > 0 || inputTokens > 0 || outputTokens > 0;
  return <div className={`run-status ${active ? "is-active" : ""} ${waiting ? "is-waiting" : ""}`}>
    <span role="status">
      <Icon size={14} aria-hidden="true" className={active && !waiting && !reconnecting ? "status-spin" : ""} />
      <span>{label}</span>
    </span>
    {(elapsed !== null || showMetrics) && <span className="run-status-metrics" aria-hidden="true">
      {elapsed !== null ? formatElapsed(elapsed) : ""}
      {cacheReadTokens > 0 ? `  ${t("run.status.cache").replace("{count}", formatTokenCount(cacheReadTokens))}` : ""}
      {(inputTokens > 0 || outputTokens > 0) ? `  ↑${formatTokenCount(inputTokens)} ↓${formatTokenCount(outputTokens)}` : ""}
    </span>}
  </div>;
}

type ElapsedSample = { runId: string; seconds: number };

/** Worker timestamps more than this far ahead of the client are clock skew. */
export const CLOCK_SKEW_TOLERANCE_MS = 5000;

/**
 * The wall-clock origin of the elapsed timer. A Worker clock can sit ahead of
 * the browser clock; using that timestamp verbatim would pin the timer at "0s"
 * for the whole run, so clear skew falls back to the client clock.
 */
export function elapsedOrigin(startedAt: string | null | undefined, now: number): number {
  const parsed = startedAt ? Date.parse(startedAt) : Number.NaN;
  if (Number.isNaN(parsed)) return now;
  if (parsed > now + CLOCK_SKEW_TOLERANCE_MS) return now;
  return parsed;
}

/** Seconds on the timer for a run whose origin was resolved once at its start. */
export function elapsedSince(origin: number, now: number): number {
  return Math.max(0, Math.floor((now - origin) / 1000));
}

/**
 * The value to display for a run. A sample belongs to exactly one run, so a
 * run change never shows the previous run's elapsed time, not even for the
 * frame before the timer effect restarts.
 */
export function displayedElapsed(
  active: boolean,
  runId: string | null | undefined,
  sample: ElapsedSample | null,
): number | null {
  if (!active || !runId) return null;
  return sample && sample.runId === runId ? sample.seconds : null;
}

function useElapsed(active: boolean, runId: string | null | undefined, startedAt: string | null | undefined): number | null {
  const [sample, setSample] = useState<ElapsedSample | null>(null);
  const originRef = useRef<{ runId: string; origin: number } | null>(null);
  useEffect(() => {
    if (!active || !runId) return;
    // The origin is resolved once per run, keyed on runId: a later, refreshed
    // startedAt for the same run must not restart the clock, and a skewed
    // Worker timestamp falls back to the moment this client first saw the run.
    // `displayedElapsed` already hides a sample tagged with the previous
    // runId, so no setState is needed to clear it here.
    if (originRef.current?.runId !== runId) {
      originRef.current = { runId, origin: elapsedOrigin(startedAt, Date.now()) };
    }
    const origin = originRef.current.origin;
    const tick = () => setSample({ runId, seconds: elapsedSince(origin, Date.now()) });
    const frame = window.requestAnimationFrame(tick);
    const timer = window.setInterval(tick, 1000);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearInterval(timer);
    };
  }, [active, runId, startedAt]);
  return displayedElapsed(active, runId, sample);
}

function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
}
