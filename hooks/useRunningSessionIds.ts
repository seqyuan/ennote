"use client";

import { useEffect, useMemo, useState } from "react";
import type { ActiveRunState } from "@/lib/approval";
import { apiFetch } from "@/lib/worker-api.client";

export function useRunningSessionIds(sessionIds: string[], activeSessionId?: string | null) {
  const [discovered, setDiscovered] = useState<Set<string>>(new Set());
  const requestKey = [...new Set(sessionIds)].sort().join("\u0000");
  const stableIds = useMemo(() => requestKey ? requestKey.split("\u0000") : [], [requestKey]);

  useEffect(() => {
    if (!requestKey) return;
    const controller = new AbortController();
    const refresh = async () => {
      const results = await Promise.all(stableIds.slice(0, 50).map(async (sessionId) => {
        try {
          const state = await apiFetch<ActiveRunState | null>(`/v1/sessions/${encodeURIComponent(sessionId)}/active-run`, { signal: controller.signal });
          return state ? sessionId : null;
        } catch {
          return null;
        }
      }));
      if (!controller.signal.aborted) setDiscovered(new Set(results.filter((value): value is string => Boolean(value))));
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 15_000);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [requestKey, stableIds]);

  return useMemo(() => {
    const result = new Set(discovered);
    if (activeSessionId) result.add(activeSessionId);
    return result;
  }, [activeSessionId, discovered]);
}
