import { describe, expect, it } from "vitest";

import {
  emptyQueuedFollowUps, mergeQueuedFollowUps, queuedFollowUpReducer, type QueuedRow,
} from "@/hooks/useQueuedFollowUps";
import type { components } from "@/lib/worker-api.gen";

type QueuedInput = components["schemas"]["QueuedInput"];

function input(id: string, text: string, kind: QueuedInput["kind"] = "follow_up"): QueuedInput {
  return {
    id, text, kind, runId: "run-1", sessionId: "s1", clientRequestId: `${id}-req`,
    seq: 1, status: "queued", createdAt: "2026-11-01T00:00:00Z",
  };
}

describe("mergeQueuedFollowUps", () => {
  it("renders the durable queue in the Worker's order", () => {
    expect(mergeQueuedFollowUps([input("a", "first"), input("b", "second")], [], new Set()))
      .toEqual([{ id: "a", text: "first" }, { id: "b", text: "second" }]);
  });

  it("appends what this tab queued before the snapshot echoed it", () => {
    const optimistic: QueuedRow[] = [{ id: "c", text: "third" }];
    expect(mergeQueuedFollowUps([input("a", "first")], optimistic, new Set()).map((row) => row.id))
      .toEqual(["a", "c"]);
  });

  it("does not draw a row twice once the snapshot carries it", () => {
    const optimistic: QueuedRow[] = [{ id: "a", text: "first" }];
    const rows = mergeQueuedFollowUps([input("a", "first")], optimistic, new Set());
    expect(rows).toEqual([{ id: "a", text: "first" }]);
  });

  it("hides an id this tab cancelled while the snapshot still lists it", () => {
    const rows = mergeQueuedFollowUps([input("a", "first"), input("b", "second")], [], new Set(["a"]));
    expect(rows.map((row) => row.id)).toEqual(["b"]);
  });

  it("hides a consumed id from both the snapshot and the optimistic tail", () => {
    const optimistic: QueuedRow[] = [{ id: "c", text: "third" }];
    const rows = mergeQueuedFollowUps([input("c", "third")], optimistic, new Set(["c"]));
    expect(rows).toEqual([]);
  });

  it("settles the id the Worker named, not the row this tab happens to show first", () => {
    // Another tab queued "b" before this one queued "c"; the Worker drains "b".
    // Settling position would have hidden "a" and left "b" visible.
    const optimistic: QueuedRow[] = [{ id: "c", text: "third" }];
    const rows = mergeQueuedFollowUps([input("a", "first"), input("b", "second")], optimistic, new Set(["b"]));
    expect(rows.map((row) => row.id)).toEqual(["a", "c"]);
  });

  it("leaves steers out: they are queued, but not editable dock rows", () => {
    const rows = mergeQueuedFollowUps([input("a", "steer me", "steer"), input("b", "follow up")], [], new Set());
    expect(rows.map((row) => row.id)).toEqual(["b"]);
  });

  it("keeps the snapshot row's text when the optimistic copy differs", () => {
    const optimistic: QueuedRow[] = [{ id: "a", text: "typed locally" }];
    const rows = mergeQueuedFollowUps([input("a", "normalised by the Worker")], optimistic, new Set());
    expect(rows[0].text).toBe("normalised by the Worker");
  });
});

describe("queuedFollowUpReducer", () => {
  // The dock renders snapshot rows first and optimistic rows last, so "the first
  // row" and "the row the Worker drained" are different rows as soon as another
  // tab has queued something older. These cases pin the id-based rule: they fail
  // against any implementation that settles by position.
  const snapshot = [input("a", "queued by another tab"), input("b", "second")];

  it("settles the id the Worker named while a snapshot row is ahead of it", () => {
    const state = queuedFollowUpReducer(emptyQueuedFollowUps, {
      type: "queue", row: { id: "c", text: "queued here" },
    });
    const settled = queuedFollowUpReducer(state, { type: "settle", id: "b" });
    expect(mergeQueuedFollowUps(snapshot, settled.optimistic, settled.settled).map((row) => row.id))
      .toEqual(["a", "c"]);
  });

  it("leaves the queue alone when an older Worker names no id", () => {
    const state = queuedFollowUpReducer(emptyQueuedFollowUps, {
      type: "queue", row: { id: "c", text: "queued here" },
    });
    const settled = queuedFollowUpReducer(state, { type: "settle", id: "" });
    expect(settled).toBe(state);
  });

  it("drops a settled id from both halves", () => {
    let state = queuedFollowUpReducer(emptyQueuedFollowUps, { type: "queue", row: { id: "c", text: "here" } });
    state = queuedFollowUpReducer(state, { type: "settle", id: "c" });
    expect(state.optimistic).toEqual([]);
    expect(mergeQueuedFollowUps([input("c", "here")], state.optimistic, state.settled)).toEqual([]);
  });

  it("reset clears the optimistic tail and every settled id", () => {
    let state = queuedFollowUpReducer(emptyQueuedFollowUps, { type: "queue", row: { id: "c", text: "here" } });
    state = queuedFollowUpReducer(state, { type: "settle", id: "a" });
    state = queuedFollowUpReducer(state, { type: "reset" });
    expect(state).toEqual(emptyQueuedFollowUps);
    expect(mergeQueuedFollowUps(snapshot, state.optimistic, state.settled).map((row) => row.id))
      .toEqual(["a", "b"]);
  });
});
