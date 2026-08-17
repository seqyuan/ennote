"use client";

import { useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { TurnMetric } from "@/lib/chat-messages";

/**
 * Loads per-turn footer readings for a session, keyed by run id, so the message
 * chrome can attach "Ran for / TTFT / tok/s" to each completed turn's tail.
 * Re-fetches on session switch and whenever the active run id changes, so a
 * just-finished turn's metrics land without waiting for the next navigation.
 */
export function useTurnMetrics(sessionId: string | null, activeRunId: string | null): Map<string, TurnMetric> {
  const [metrics, setMetrics] = useState<Map<string, TurnMetric>>(new Map());

  useEffect(() => {
    if (!sessionId) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- reset on session clear
      setMetrics(new Map());
      return;
    }
    let cancelled = false;
    apiFetch<TurnMetric[]>(`/v1/sessions/${encodeURIComponent(sessionId)}/turn-metrics`)
      .then((rows) => {
        if (cancelled) return;
        setMetrics(new Map(rows.map((row) => [row.runId, row])));
      })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [sessionId, activeRunId]);

  return metrics;
}
