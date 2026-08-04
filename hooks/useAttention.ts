"use client";

import { useEffect, useMemo, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type AttentionItem = components["schemas"]["AttentionItem"];

const pollIntervalMs = 5000;

// useAttention polls the current project's pending attention items. Rows are
// deduped by id, so reconnect replay never duplicates; the canonical attention
// state is always re-fetched after any action.
export function useAttention(projectId: string | undefined): {
  items: AttentionItem[];
  pendingCount: number;
  refresh: () => void;
} {
  const [items, setItems] = useState<AttentionItem[]>([]);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (!projectId) return;
    const controller = new AbortController();
    let timer: number | undefined;
    const poll = async () => {
      try {
        const page = await apiFetch<{ items: AttentionItem[] }>(
          `/v1/attention?projectId=${encodeURIComponent(projectId)}&status=pending&limit=50`,
          { signal: controller.signal },
        );
        if (!controller.signal.aborted) setItems(page.items ?? []);
      } catch {
        // Polling is progressive; transient failures are silent.
      } finally {
        if (!controller.signal.aborted) timer = window.setTimeout(poll, pollIntervalMs);
      }
    };
    void poll();
    return () => {
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [projectId, tick]);

  const pendingCount = useMemo(() => items.filter(item => item.status === "pending").length, [items]);
  const refresh = () => setTick(value => value + 1);
  return { items, pendingCount, refresh };
}
