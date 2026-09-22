"use client";

import { useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

export type RunPromptComposition = components["schemas"]["RunPromptComposition"];

/**
 * Reads one Run's frozen prompt composition. The Worker recorded it before the
 * first Provider request, so this is a projection of an immutable fact, never a
 * re-derivation from current configuration.
 *
 * `runId === null` clears the panel without a request.
 */
export function useRunPromptComposition(runId: string | null): {
  composition: RunPromptComposition | null;
  loading: boolean;
  error: string | null;
} {
  const [composition, setComposition] = useState<RunPromptComposition | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!runId) {
      const clear = window.setTimeout(() => {
        setComposition(null);
        setError(null);
        setLoading(false);
      }, 0);
      return () => window.clearTimeout(clear);
    }
    const controller = new AbortController();
    const start = window.setTimeout(() => {
      setLoading(true);
      apiFetch<RunPromptComposition>(`/v1/runs/${encodeURIComponent(runId)}/prompt`, { signal: controller.signal })
        .then((next) => {
          if (controller.signal.aborted) return;
          setComposition(next);
          setError(null);
        })
        .catch((reason: unknown) => {
          if (controller.signal.aborted) return;
          setComposition(null);
          setError(reason instanceof Error ? reason.message : "Failed to load the run prompt");
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

  return { composition, loading, error };
}
