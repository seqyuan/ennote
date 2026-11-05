"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import {
  planHandleRefresh,
  type BackgroundDelivery,
  type DelegationCompletion,
  type DelegationHandle,
} from "@/lib/delegation-delivery";

type HandlePage = { items: DelegationHandle[]; nextCursor?: string };

export type { BackgroundDelivery };

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
  // The poll loop reads the cache it is about to replace; a ref keeps that read
  // off the effect's dependency list (a cache dependency would restart the timer
  // on every tick).
  const cacheRef = useRef<DeliveryCache>(emptyCache);
  useEffect(() => { cacheRef.current = cache; }, [cache]);

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
        // are pruned instead of lingering as stale rows. Rows the previous tick
        // already answered are reused, so a steady state costs one request.
        const known = cacheRef.current.session === scope ? cacheRef.current.rows : {};
        const plan = planHandleRefresh(known, scope, page.items);
        const listed = plan.rows;
        // Only the handles that actually moved need their completion fetched.
        await Promise.all(plan.toFetch.map(async handle => {
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
            const previousRow = previous.rows[id];
            // A failed detail fetch must not erase a completion already seen
            // for this session; anything else is taken from the fresh listing.
            rows[id] = !row.completion && previousRow?.handle.sessionId === scope ? previousRow : row;
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
