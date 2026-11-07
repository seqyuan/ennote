"use client";

import { useCallback, useMemo, useReducer } from "react";
import type { components } from "@/lib/worker-api.gen";

type QueuedInput = components["schemas"]["QueuedInput"];

/** The two fields the composer dock renders. */
export interface QueuedRow {
  id: string;
  text: string;
}

/**
 * What this tab knows about the queue that the snapshot does not: the follow-ups
 * it queued a frame ago, and the ids it has already seen settled.
 */
export interface QueuedFollowUpState {
  optimistic: QueuedRow[];
  settled: ReadonlySet<string>;
}

export type QueuedFollowUpAction =
  | { type: "queue"; row: QueuedRow }
  | { type: "settle"; id: string }
  | { type: "reset" };

export const emptyQueuedFollowUps: QueuedFollowUpState = { optimistic: [], settled: new Set() };

/**
 * The queue's local bookkeeping, as a pure reducer.
 *
 * `settle` takes an id and nothing else on purpose: the Worker names the input it
 * drained (queueItemId), and a position would be wrong as soon as another tab, or
 * a reload, has queued an older one. Keeping that rule in a reducer is what makes
 * it testable without a DOM.
 */
export function queuedFollowUpReducer(
  state: QueuedFollowUpState,
  action: QueuedFollowUpAction,
): QueuedFollowUpState {
  switch (action.type) {
    case "queue":
      return { ...state, optimistic: [...state.optimistic, action.row] };
    case "settle": {
      // An older Worker sends no id; the snapshot still drops the row, so there is
      // nothing to settle and nothing to guess.
      if (!action.id) return state;
      const settled = new Set(state.settled);
      settled.add(action.id);
      const optimistic = state.optimistic.filter((row) => row.id !== action.id);
      return { optimistic, settled };
    }
    case "reset":
      return emptyQueuedFollowUps;
  }
}

/**
 * The dock's rows: the durable queue in the Worker's injection order, then the
 * optimistic additions whose ids have not landed yet. An id this tab settled
 * (cancelled or seen consumed) is dropped from both, because the snapshot can be a
 * frame behind and must not draw it again.
 */
export function mergeQueuedFollowUps(
  queuedInputs: QueuedInput[],
  optimistic: QueuedRow[],
  settled: ReadonlySet<string>,
): QueuedRow[] {
  const result: QueuedRow[] = [];
  const seen = new Set<string>();
  for (const input of queuedInputs) {
    // The dock lists follow-ups (the ones it can edit, remove and steer); steers
    // are in the same snapshot but are not editable queued messages.
    if (input.kind !== "follow_up" || settled.has(input.id) || seen.has(input.id)) continue;
    seen.add(input.id);
    result.push({ id: input.id, text: input.text });
  }
  for (const row of optimistic) {
    if (settled.has(row.id) || seen.has(row.id)) continue;
    seen.add(row.id);
    result.push(row);
  }
  return result;
}

/**
 * The composer dock's queue: the Worker's queued follow-ups, plus what this tab
 * has just done to them.
 *
 * The session change feed is authoritative — a reload, a second tab, or a
 * reconnect must show the same queue — but it arrives a frame behind the click.
 * So the hook keeps the optimistic additions this tab made and the ids it has
 * already cancelled or seen consumed, and renders the durable list first with the
 * optimistic tail after it. Nothing here decides what is queued; it only explains
 * the gap between the click and the snapshot.
 */
export function useQueuedFollowUps(queuedInputs: QueuedInput[]) {
  const [state, dispatch] = useReducer(queuedFollowUpReducer, emptyQueuedFollowUps);

  /** A follow-up this tab just queued, before the snapshot echoes it. */
  const queue = useCallback((row: QueuedRow) => dispatch({ type: "queue", row }), []);

  /**
   * The Worker consumed or cancelled one queued input. Its id is settled so the
   * row disappears from both halves of the list, even if the snapshot still
   * carries it for another frame.
   */
  const settle = useCallback((id: string) => dispatch({ type: "settle", id }), []);

  const reset = useCallback(() => dispatch({ type: "reset" }), []);

  const rows = useMemo(
    () => mergeQueuedFollowUps(queuedInputs, state.optimistic, state.settled),
    [queuedInputs, state.optimistic, state.settled],
  );

  return { rows, queue, settle, reset };
}
