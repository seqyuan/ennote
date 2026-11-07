"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
  // A mirror the stream callbacks can read. They run outside render and must not
  // take the optimistic list in their identity, or the run stream would restart
  // on every queue change.
  const optimisticRef = useRef<QueuedRow[]>([]);

  useEffect(() => {
    optimisticRef.current = optimistic;
  }, [optimistic]);

  /** A follow-up this tab just queued, before the snapshot echoes it. */
  const queue = useCallback((row: QueuedRow) => {
    setOptimistic(current => [...current, row]);
  }, []);

  /**
   * The Worker consumed the oldest queued input. It is gone from the queue even if
   * the snapshot still lists it, so settle its id before it can be drawn again.
   */
  const consumeOldest = useCallback(() => {
    const head = optimisticRef.current[0];
    if (!head) return;
    setSettled(ids => new Set(ids).add(head.id));
    setOptimistic(current => (current.length === 0 ? current : current.slice(1)));
  }, []);

  /** One queued input was cancelled; hide it until the snapshot drops it too. */
  const cancel = useCallback((id: string) => {
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

  return { rows, queue, consumeOldest, cancel, reset };
}
