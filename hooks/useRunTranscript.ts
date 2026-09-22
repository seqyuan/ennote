"use client";

import { useCallback, useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

export type RunMessagePage = components["schemas"]["RunMessagePage"];

const PAGE_SIZE = 200;

/**
 * Reads a Run's private transcript: the model-visible messages the Run itself
 * generated, used by the Worker to resume a suspended Run with an intact
 * provider protocol.
 *
 * This is deliberately NOT the conversation timeline. Timeline messages are
 * public Session history; these rows are Run-private, so they are read on demand
 * and never merged into the transcript a reader scrolls.
 */
export function useRunTranscript(runId: string | null): {
  messages: RunMessagePage["messages"];
  hasMore: boolean;
  loading: boolean;
  loadingOlder: boolean;
  error: string | null;
  loadOlder: () => void;
} {
  const [messages, setMessages] = useState<RunMessagePage["messages"]>([]);
  const [nextBefore, setNextBefore] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!runId) {
      const clear = window.setTimeout(() => {
        setMessages([]);
        setNextBefore(null);
        setError(null);
        setLoading(false);
      }, 0);
      return () => window.clearTimeout(clear);
    }
    const controller = new AbortController();
    const start = window.setTimeout(() => {
      setLoading(true);
      apiFetch<RunMessagePage>(`/v1/runs/${encodeURIComponent(runId)}/messages?limit=${PAGE_SIZE}`,
        { signal: controller.signal })
        .then((page) => {
          if (controller.signal.aborted) return;
          setMessages(page.messages ?? []);
          setNextBefore(page.hasMore ? (page.nextBeforeOrdinal ?? null) : null);
          setError(null);
        })
        .catch((reason: unknown) => {
          if (controller.signal.aborted) return;
          setMessages([]);
          setNextBefore(null);
          setError(reason instanceof Error ? reason.message : "Failed to load the run transcript");
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

  // Older pages prepend; the Worker reports a non-overlapping ordinal range, so
  // an ordinal already held is dropped rather than duplicated.
  const loadOlder = useCallback(() => {
    if (!runId || nextBefore === null) return;
    setLoadingOlder(true);
    apiFetch<RunMessagePage>(
      `/v1/runs/${encodeURIComponent(runId)}/messages?limit=${PAGE_SIZE}&beforeOrdinal=${nextBefore}`)
      .then((page) => {
        setMessages((current) => {
          const known = new Set(current.map((message) => message.ordinal));
          return [...(page.messages ?? []).filter((message) => !known.has(message.ordinal)), ...current];
        });
        setNextBefore(page.hasMore ? (page.nextBeforeOrdinal ?? null) : null);
        setError(null);
      })
      .catch((reason: unknown) => {
        setError(reason instanceof Error ? reason.message : "Failed to load older transcript messages");
      })
      .finally(() => setLoadingOlder(false));
  }, [runId, nextBefore]);

  return { messages, hasMore: nextBefore !== null, loading, loadingOlder, error, loadOlder };
}
