"use client";

import { useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { SessionStats } from "@/hooks/chat-controller-types";

/**
 * Loads the Worker-computed aggregate session statistics for the composer
 * StatsLine. Re-fetches on session switch and whenever the active run id
 * changes (so a finished run's figures land without waiting for the next
 * navigation). The Worker computes these from durable model_calls/tool_calls,
 * so paging and compaction cannot change them.
 */
export function useSessionStats(sessionId: string | null, activeRunId: string | null): SessionStats | null {
  const [stats, setStats] = useState<SessionStats | null>(null);

  useEffect(() => {
    if (!sessionId) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- reset on session clear
      setStats(null);
      return;
    }
    let cancelled = false;
    const load = (): void => {
      apiFetch<SessionStats>(`/v1/sessions/${encodeURIComponent(sessionId)}/stats`)
        .then((value) => { if (!cancelled) setStats(value); })
        .catch(() => {});
    };
    void load();
    // Keep the line ticking while a run is active; the endpoint aggregates the
    // durable model_calls/tool_calls, so it reflects completed steps live.
    const timer = activeRunId ? window.setInterval(load, 2000) : undefined;
    return () => {
      cancelled = true;
      if (timer !== undefined) window.clearInterval(timer);
    };
  }, [sessionId, activeRunId]);

  return stats;
}
