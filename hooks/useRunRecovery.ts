"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { components } from "@/lib/worker-api.gen";
import { apiFetch } from "@/lib/worker-api.client";

type RunRecovery = components["schemas"]["RunRecovery"];
type RunRetrySubmission = components["schemas"]["RunRetrySubmission"];

function genId(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}

export function useRunRecovery(sessionId: string | null, lineageId?: string, activeRunId?: string | null) {
  const [recovery, setRecovery] = useState<RunRecovery | null>(null);
  const [dataLineageId, setDataLineageId] = useState<string | undefined>();
  const [retrying, setRetrying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    if (!sessionId || activeRunId) {
      setRecovery(null);
      return;
    }
    const version = generation.current;
    try {
      const value = await apiFetch<RunRecovery | null>(`/v1/sessions/${encodeURIComponent(sessionId)}/recovery`, { signal });
      if (!signal?.aborted && generation.current === version) {
        setRecovery(value);
        setDataLineageId(lineageId);
        setError(null);
      }
    } catch (cause) {
      if (!signal?.aborted && generation.current === version) setError((cause as Error).message);
    }
  }, [activeRunId, lineageId, sessionId]);

  useEffect(() => {
    const version = ++generation.current;
    const controller = new AbortController();
    queueMicrotask(() => {
      if (generation.current !== version) return;
      setRecovery(null);
      setDataLineageId(undefined);
      setError(null);
      void refresh(controller.signal);
    });
    return () => controller.abort();
  }, [lineageId, refresh]);

  const retry = useCallback(async () => {
    if (!recovery?.retryable || retrying) return null;
    setRetrying(true);
    try {
      const requestId = genId();
      const submission = await apiFetch<RunRetrySubmission>(`/v1/runs/${encodeURIComponent(recovery.run.id)}/retry`, {
        method: "POST", headers: { "Content-Type": "application/json", "Idempotency-Key": requestId },
        body: JSON.stringify({ clientRequestId: requestId }),
      });
      setRecovery(null);
      setError(null);
      return submission.run;
    } catch (cause) {
      setError((cause as Error).message);
      return null;
    } finally {
      setRetrying(false);
    }
  }, [recovery, retrying]);

  const clearError = useCallback(() => setError(null), []);
  return { recovery: dataLineageId === lineageId ? recovery : null, retrying, error, clearError, retry, refresh };
}
