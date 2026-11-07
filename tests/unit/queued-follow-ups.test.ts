import { describe, expect, it } from "vitest";

import { mergeQueuedFollowUps, type QueuedRow } from "@/hooks/useQueuedFollowUps";
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
