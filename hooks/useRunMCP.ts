"use client";

import { useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

export type RunMCPFrozen = components["schemas"]["RunMCPFrozen"];

/**
 * Reads the MCP surface a Run froze before its first Provider request: which
 * servers it reached and which tools they contributed.
 *
 * `runId === null` clears the section without a request.
 */
export function useRunMCP(runId: string | null): {
  frozen: RunMCPFrozen | null;
  loading: boolean;
  error: string | null;
} {
  const [frozen, setFrozen] = useState<RunMCPFrozen | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!runId) {
      const clear = window.setTimeout(() => {
        setFrozen(null);
        setError(null);
        setLoading(false);
      }, 0);
      return () => window.clearTimeout(clear);
    }
    const controller = new AbortController();
    const start = window.setTimeout(() => {
      setLoading(true);
      apiFetch<RunMCPFrozen>(`/v1/runs/${encodeURIComponent(runId)}/mcp`, { signal: controller.signal })
        .then((next) => {
          if (controller.signal.aborted) return;
          setFrozen(next);
          setError(null);
        })
        .catch((reason: unknown) => {
          if (controller.signal.aborted) return;
          setFrozen(null);
          setError(reason instanceof Error ? reason.message : "Failed to load the run MCP surface");
        })
        .finally(() => {
          if (!controller.signal.aborted) setLoading(false);
        });
    }, 0);
    return () => {
      window.clearTimeout(start);
      controller.abort();
    };
  }, [runId]);

  return { frozen, loading, error };
}
