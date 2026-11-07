"use client";

import { useCallback, useMemo, useState } from "react";
import type { components } from "@/lib/worker-api.gen";

type QueuedInput = components["schemas"]["QueuedInput"];

/** The two fields the composer dock renders. */
export interface QueuedRow {
  id: string;
  text: string;
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
  const [optimistic, setOptimistic] = useState<QueuedRow[]>([]);
  const [settled, setSettled] = useState<Set<string>>(() => new Set());
  /** A follow-up this tab just queued, before the snapshot echoes it. */
  const queue = useCallback((row: QueuedRow) => {
    setOptimistic(current => [...current, row]);
  }, []);

  /**
   * The Worker consumed or cancelled one queued input. Its id is settled so the
   * row disappears from both halves of the list, even if the snapshot still
   * carries it for another frame.
   *
   * The id comes from the run event (queueItemId). It is not inferred from
   * position: the message the Worker drained need not be the newest row this tab
   * is showing, because another tab or a reload can have queued an older one.
   */
  const settle = useCallback((id: string) => {
    if (!id) return;
    setSettled(ids => new Set(ids).add(id));
    setOptimistic(current => current.filter(item => item.id !== id));
  }, []);

  const reset = useCallback(() => {
    setSettled(new Set());
    setOptimistic([]);
  }, []);

  const rows = useMemo(
    () => mergeQueuedFollowUps(queuedInputs, optimistic, settled),
    [queuedInputs, optimistic, settled],
  );

  return { rows, queue, settle, reset };
}
