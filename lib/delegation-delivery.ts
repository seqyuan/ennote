/**
 * Delivery-strip polling plan.
 *
 * The strip used to ask for a detail row per handle on every tick, which is an
 * N+1 on a session with several background delegations and a request per handle
 * every 4 seconds forever. A handle carries `status` and `updatedAt`, and the
 * detail endpoint adds the completion, so an unchanged handle can be answered
 * from the previous tick: the poll then costs one request per tick plus one per
 * handle that actually moved.
 */
import type { components } from "@/lib/worker-api.gen";

export type DelegationHandle = components["schemas"]["DelegationHandle"];
export type DelegationCompletion = components["schemas"]["DelegationCompletion"];

export type BackgroundDelivery = {
  handle: DelegationHandle;
  completion?: DelegationCompletion;
};

/**
 * Should this handle's detail row be fetched again?
 *
 * `updatedAt` moves whenever the Worker touches the handle, so an unchanged
 * stamp means the detail row cannot have gained anything. The exception is a
 * completed handle whose completion is still missing — the previous detail call
 * may simply have failed — so that one is retried until it lands.
 */
export function shouldFetchHandleDetail(previous: BackgroundDelivery | undefined, listed: DelegationHandle): boolean {
  if (!previous) return true;
  if (previous.handle.updatedAt !== listed.updatedAt) return true;
  return !previous.completion && listed.status === "completed";
}

export type HandleRefreshPlan = {
  /** Rows answered from the previous tick, keyed by handle id. */
  rows: Record<string, BackgroundDelivery>;
  /** Handles whose detail must be fetched before the plan is complete. */
  toFetch: DelegationHandle[];
};

/**
 * Splits a handle listing into rows that can be reused and handles that need a
 * detail request. Handles belonging to another session are dropped here rather
 * than filtered at render time, so a cross-session row cannot enter the cache.
 */
export function planHandleRefresh(
  previous: Record<string, BackgroundDelivery>,
  sessionId: string,
  items: readonly DelegationHandle[],
): HandleRefreshPlan {
  const plan: HandleRefreshPlan = { rows: {}, toFetch: [] };
  for (const listed of items) {
    if (listed.sessionId && listed.sessionId !== sessionId) continue;
    const known = previous[listed.id];
    if (known && known.handle.sessionId === sessionId && !shouldFetchHandleDetail(known, listed)) {
      plan.rows[listed.id] = known;
      continue;
    }
    plan.toFetch.push(listed);
  }
  return plan;
}
