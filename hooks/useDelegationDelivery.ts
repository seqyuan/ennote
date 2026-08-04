"use client";

import { useEffect, useMemo, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type DelegationHandle = components["schemas"]["DelegationHandle"];
type DelegationCompletion = components["schemas"]["DelegationCompletion"];
type HandlePage = { items: DelegationHandle[]; nextCursor?: string };

export type BackgroundDelivery = {
  handle: DelegationHandle;
  completion?: DelegationCompletion;
};

const pollIntervalMs = 4000;

// useDelegationDelivery polls the current session's background delegation
// handles and their latest completion. Rows are deduped by handle id, so a
// reconnect or replay never duplicates a logical completion. The hook never
// mutates execution state — it is a pure projection for the strip.
export function useDelegationDelivery(sessionId: string | undefined): {
  deliveries: BackgroundDelivery[];
  refreshing: boolean;
} {
  const [rows, setRows] = useState<Record<string, BackgroundDelivery>>({});
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    if (!sessionId) return;
    const controller = new AbortController();
    let timer: number | undefined;
    let disposed = false;

    const refresh = async () => {
      try {
        setRefreshing(true);
        const page = await apiFetch<HandlePage>(
          `/v1/sessions/${encodeURIComponent(sessionId)}/delegation-handles?limit=20`,
          { signal: controller.signal },
        );
        if (disposed || controller.signal.aborted) return;
        const next = { ...rows };
        // Poll each handle's latest completion in parallel.
        await Promise.all(page.items.map(async handle => {
          try {
            const detail = await apiFetch<{ handle: DelegationHandle; completion?: DelegationCompletion }>(
              `/v1/delegation-handles/${encodeURIComponent(handle.id)}`,
              { signal: controller.signal },
            );
            if (disposed || controller.signal.aborted) return;
            next[handle.id] = { handle: detail.handle, completion: detail.completion };
          } catch {
            // Keep the row from the plain listing when detail fails.
            if (!next[handle.id]) next[handle.id] = { handle };
          }
        }));
        if (disposed || controller.signal.aborted) return;
        setRows(next);
      } catch {
        // Polling is progressive; silence transient failures.
      } finally {
        if (!disposed) setRefreshing(false);
        timer = window.setTimeout(refresh, pollIntervalMs);
      }
    };
    void refresh();
    return () => {
      disposed = true;
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId]);

  const deliveries = useMemo(() => {
    return Object.values(rows).sort((a, b) =>
      (a.handle.createdAt ?? "").localeCompare(b.handle.createdAt ?? ""));
  }, [rows]);

  return { deliveries, refreshing };
}
