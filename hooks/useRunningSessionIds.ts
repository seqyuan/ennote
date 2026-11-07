"use client";

import { useEffect, useMemo, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";

interface ProjectActiveRuns {
  sessionIds: string[];
}

const REFRESH_INTERVAL_MS = 15_000;

/**
 * The Sessions that currently own an active Run, for the sidebar's running
 * marker.
 *
 * One poll per Project: a Session's Run state lives in that Session's database,
 * so asking per Session turned a sidebar of N Sessions into N requests every
 * interval. The Worker answers the whole Project in one response
 * (GET /v1/projects/{id}/active-runs) over the same predicate the per-Session
 * endpoint uses.
 *
 * The selected Session is added locally while its own run is live, so the marker
 * does not wait for the next poll.
 */
export function useRunningSessionIds(projectIds: string[], activeSessionId?: string | null) {
  const [discovered, setDiscovered] = useState<Set<string>>(new Set());
  const requestKey = [...new Set(projectIds)].sort().join("\u0000");

  useEffect(() => {
    if (!requestKey) return;
    const ids = requestKey.split("\u0000");
    const controller = new AbortController();
    const refresh = async () => {
      const answers = await Promise.all(ids.map(async (projectId) => {
        try {
          const answer = await apiFetch<ProjectActiveRuns>(
            `/v1/projects/${encodeURIComponent(projectId)}/active-runs`, { signal: controller.signal });
          return answer?.sessionIds ?? [];
        } catch {
          return [];
        }
      }));
      if (!controller.signal.aborted) setDiscovered(new Set(answers.flat()));
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), REFRESH_INTERVAL_MS);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [requestKey]);

  return useMemo(() => {
    // A poll for a previous Project set may still be in flight; its answers are
    // ignored rather than briefly marking the wrong rows as running.
    const result = new Set(requestKey ? discovered : []);
    if (activeSessionId) result.add(activeSessionId);
    return result;
  }, [activeSessionId, discovered, requestKey]);
}
