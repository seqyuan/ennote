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

/** Row cache tagged with the session it belongs to, so a session switch can
 *  never read the previous session's completions. */
type DeliveryCache = {
  session: string | undefined;
  rows: Record<string, BackgroundDelivery>;
};

const pollIntervalMs = 4000;
const emptyCache: DeliveryCache = { session: undefined, rows: {} };

// useDelegationDelivery polls the current session's background delegation
// handles and their latest completion. Rows are deduped by handle id, so a
// reconnect or replay never duplicates a logical completion. The hook never
// mutates execution state — it is a pure projection for the strip.
//
// Rows are session-scoped: the cache is replaced (never merged) when the
// session changes, every accepted handle is filtered by `handle.sessionId`, and
// reads are gated on the cache tag, so session A's completions cannot render in
// session B.
export function useDelegationDelivery(sessionId: string | undefined): {
  deliveries: BackgroundDelivery[];
  refreshing: boolean;
} {
  const [cache, setCache] = useState<DeliveryCache>(emptyCache);
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    if (!sessionId) return;
    const scope = sessionId;
    const controller = new AbortController();
    let timer: number | undefined;
    let disposed = false;

    const refresh = async () => {
      try {
        setRefreshing(true);
        const page = await apiFetch<HandlePage>(
          `/v1/sessions/${encodeURIComponent(scope)}/delegation-handles?limit=20`,
          { signal: controller.signal },
        );
        if (disposed || controller.signal.aborted) return;
        // Build the listing from scratch: handles the Worker no longer returns
        // are pruned instead of lingering as stale rows.
        const listed: Record<string, BackgroundDelivery> = {};
        // Poll each handle's latest completion in parallel.
        await Promise.all(page.items.map(async handle => {
          if (handle.sessionId && handle.sessionId !== scope) return;
          try {
            const detail = await apiFetch<{ handle: DelegationHandle; completion?: DelegationCompletion }>(
              `/v1/delegation-handles/${encodeURIComponent(handle.id)}`,
              { signal: controller.signal },
            );
            if (disposed || controller.signal.aborted) return;
            if (detail.handle.sessionId && detail.handle.sessionId !== scope) return;
            listed[handle.id] = { handle: detail.handle, completion: detail.completion };
          } catch {
            // Keep the row from the plain listing when detail fails.
            listed[handle.id] = { handle };
          }
        }));
        if (disposed || controller.signal.aborted) return;
        setCache(previous => {
          const rows: Record<string, BackgroundDelivery> = {};
          for (const [id, row] of Object.entries(listed)) {
            const known = previous.rows[id];
            // A failed detail fetch must not erase a completion already seen
            // for this session; anything else is taken from the fresh listing.
            rows[id] = !row.completion && known?.handle.sessionId === scope ? known : row;
          }
          return { session: scope, rows };
        });
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
  }, [sessionId]);

  const deliveries = useMemo(() => {
    if (cache.session !== sessionId) return [];
    return Object.values(cache.rows)
      .filter(row => row.handle.sessionId === sessionId)
      .sort((a, b) => (a.handle.createdAt ?? "").localeCompare(b.handle.createdAt ?? ""));
  }, [cache, sessionId]);

  return { deliveries, refreshing };
}
