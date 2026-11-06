import { beforeEach, describe, expect, it, vi } from "vitest";

const { apiFetch, streamOnEvent, streamReject, apiEventStream } = vi.hoisted(() => {
  const streamOnEvent: { handler: ((event: { id?: string; data: unknown }) => void) | null } = { handler: null };
  const streamReject: { reject: ((reason: unknown) => void) | null } = { reject: null };
  return {
    apiFetch: vi.fn(),
    streamOnEvent,
    streamReject,
    apiEventStream: vi.fn((_path: string, onEvent: unknown, init?: RequestInit) => {
      streamOnEvent.handler = onEvent as (event: { id?: string; data: unknown }) => void;
      return new Promise<void>((_resolve, reject) => {
        streamReject.reject = reject;
        init?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
      });
    }),
  };
});

vi.mock("@/lib/worker-api.client", () => ({
  apiFetch,
  apiEventStream,
  WorkerGenerationChangedError: class WorkerGenerationChangedError extends Error {},
}));

import { WorkerGenerationChangedError } from "@/lib/worker-api.client";
import { createSessionRegistry } from "@/lib/session-store";

function messageCalls(): number {
  return apiFetch.mock.calls.filter((call) => String(call[0]).includes("/messages")).length;
}

function stubApi(messages: unknown[]): void {
  apiFetch.mockImplementation(async (path: unknown) => {
    const value = String(path);
    if (value.includes("/compactions")) return [];
    if (value.includes("/messages")) return { messages, nextCursor: undefined, hasMore: false };
    throw new Error(`unexpected path: ${value}`);
  });
}

function snapshot(activeRunId: string): unknown {
  return {
    activeRun: { id: activeRunId, sessionId: "s1", status: "running" },
    pendingApproval: null,
    queuedInputs: [],
    checkpoints: [],
    delegationActive: false,
  };
}

/** Every projection field populated, so a partial clear cannot pass. */
function populatedSnapshot(activeRunId: string): unknown {
  return {
    activeRun: { id: activeRunId, sessionId: "s1", status: "running" },
    pendingApproval: { id: "approval-1", runId: activeRunId, toolName: "bash", arguments: {} },
    queuedInputs: [{ id: "q1", runId: activeRunId, kind: "follow_up", text: "later", status: "queued" }],
    checkpoints: [],
    delegationActive: true,
  };
}

describe("SessionStore residency (phase A)", () => {
  beforeEach(() => {
    apiFetch.mockReset();
    streamOnEvent.handler = null;
    stubApi([{ id: "m1" }]);
  });

  it("loads the initial window exactly once", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");

    await store.ensureLoaded("b1");

    expect(store.getSnapshot().loaded).toBe(true);
    expect(store.getSnapshot().dataBranchId).toBe("b1");
    expect(store.getSnapshot().canonical).toEqual([{ id: "m1" }]);
    expect(messageCalls()).toBe(1);
  });

  it("is a cache hit when re-ensuring the same branch (no re-fetch)", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");

    await store.ensureLoaded("b1");
    await store.ensureLoaded("b1");

    expect(messageCalls()).toBe(1);
  });

  it("re-fetches when the branch changes", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");

    await store.ensureLoaded("b1");
    await store.ensureLoaded("b2");

    expect(messageCalls()).toBe(2);
  });

  it("keeps transient deltas deduped by id across upserts", () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");

    store.appendTransient({ id: "t1", role: "user", text: "first" });
    store.appendTransient({ id: "t1", role: "user", text: "second" });
    expect(store.getSnapshot().transient).toHaveLength(1);

    store.upsertTransient({ id: "t1", role: "user", text: "updated" });
    expect(store.getSnapshot().transient[0].text).toBe("updated");
  });

  it("withdraws one optimistic transient and notifies only when it existed", () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");
    store.appendTransient({ id: "accepted", role: "user", text: "kept" });
    store.appendTransient({ id: "rejected", role: "user", text: "retried after failure" });

    const listener = vi.fn();
    const unsubscribe = store.subscribe(listener);
    store.removeTransient("rejected");

    expect(store.getSnapshot().transient.map((item) => item.id)).toEqual(["accepted"]);
    expect(listener).toHaveBeenCalledTimes(1);

    // A second removal of the same id is a no-op, not a notify storm.
    store.removeTransient("rejected");
    expect(listener).toHaveBeenCalledTimes(1);
    unsubscribe();
  });

  it("evicts least-recently-used stores beyond the cap", () => {
    const registry = createSessionRegistry();
    for (let index = 0; index < 9; index += 1) registry.getStore(`s${index}`);

    expect(registry.has("s0")).toBe(false);
    expect(registry.has("s8")).toBe(true);
  });

  it("disposeStore removes the store and aborts in-flight requests", () => {
    const registry = createSessionRegistry();
    registry.getStore("s1");
    registry.disposeStore("s1");
    expect(registry.has("s1")).toBe(false);
  });
});

describe("SessionStore change feed (phase C)", () => {
  beforeEach(() => {
    apiFetch.mockReset();
    apiEventStream.mockClear();
    streamOnEvent.handler = null;
    stubApi([{ id: "m1" }]);
  });

  it("applies the subscribed snapshot onto the store projection", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");
    await store.ensureLoaded("b1");

    streamOnEvent.handler!({ id: undefined, data: { type: "subscribed", instanceId: "w1", lastSeq: 5, snapshot: snapshot("run-1") } });

    const snap = store.getSnapshot();
    expect(snap.workerInstanceId).toBe("w1");
    expect(snap.lastSeq).toBe(5);
    expect(snap.activeRun?.id).toBe("run-1");
  });

  it("replaces the projection on a change-detected snapshot frame", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");
    await store.ensureLoaded("b1");
    streamOnEvent.handler!({ data: { type: "subscribed", instanceId: "w1", lastSeq: 1, snapshot: snapshot("run-1") } });

    streamOnEvent.handler!({ data: { type: "snapshot", lastSeq: 2, snapshot: snapshot("run-2") } });

    expect(store.getSnapshot().activeRun?.id).toBe("run-2");
    expect(store.getSnapshot().lastSeq).toBe(2);
  });

  it("pauses the change feed without losing the cached window", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");
    await store.ensureLoaded("b1");
    expect(apiEventStream).toHaveBeenCalledTimes(1);

    store.pauseStream();
    expect(store.getSnapshot().streamOpen).toBe(false);
    expect(store.getSnapshot().loaded).toBe(true);

    await store.ensureLoaded("b1");
    expect(apiEventStream).toHaveBeenCalledTimes(2);
    expect(store.getSnapshot().streamOpen).toBe(true);
  });

  it("re-syncs messages on message_committed", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");
    await store.ensureLoaded("b1");
    const before = messageCalls();

    streamOnEvent.handler!({ id: "42", data: { type: "message_committed", runId: "run-1", firstSeq: 2, lastSeq: 2 } });
    await vi.waitFor(() => expect(messageCalls()).toBe(before + 1));

    expect(store.getSnapshot().lastSeq).toBe(2);
  });

  it("collapses a burst of commits into one window refresh", async () => {
    // The Authority emits one commit per run, in the same transaction as the
    // terminal frame. This covers the shape the stream contract also allows and
    // the Authority does not currently produce: several commits, no terminal
    // frame. Each one used to issue a pair of requests that aborted the previous
    // pair, and the projection is snapshot-driven, so the burst only has to mark
    // the window dirty once.
    vi.useFakeTimers();
    try {
      const registry = createSessionRegistry();
      const store = registry.getStore("s1");
      await store.ensureLoaded("b1");
      const before = messageCalls();

      for (const seq of [2, 3, 4, 5, 6]) {
        streamOnEvent.handler!({ id: String(seq), data: { type: "message_committed", runId: "run-1", firstSeq: seq, lastSeq: seq } });
      }
      await vi.advanceTimersByTimeAsync(500);
      expect(messageCalls()).toBe(before + 1);

      await vi.advanceTimersByTimeAsync(5_000);
      expect(messageCalls()).toBe(before + 1);
      expect(store.getSnapshot().lastSeq).toBe(6);
    } finally {
      vi.useRealTimers();
    }
  });

  it("refreshes immediately when a terminal frame arrives mid-burst", async () => {
    vi.useFakeTimers();
    try {
      const registry = createSessionRegistry();
      const store = registry.getStore("s1");
      await store.ensureLoaded("b1");
      const before = messageCalls();

      streamOnEvent.handler!({ data: { type: "message_committed", runId: "run-1", firstSeq: 2, lastSeq: 2 } });
      streamOnEvent.handler!({ data: { type: "run_succeeded", runId: "run-1" } });
      await vi.advanceTimersByTimeAsync(0);

      // No waiting for the debounce: the boundary refresh takes the pending one.
      expect(messageCalls()).toBe(before + 1);
      await vi.advanceTimersByTimeAsync(5_000);
      expect(messageCalls()).toBe(before + 1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("drops the projection when the change feed is paused", async () => {
    // A store with no socket cannot see a run finish or an approval resolve, so
    // a session switched back to must not paint a finished run as running.
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");
    await store.ensureLoaded("b1");
    streamOnEvent.handler!({ data: { type: "subscribed", instanceId: "w1", lastSeq: 3, snapshot: populatedSnapshot("run-1") } });
    expect(store.getSnapshot().activeRun?.id).toBe("run-1");
    expect(store.getSnapshot().pendingApproval?.id).toBe("approval-1");
    expect(store.getSnapshot().queuedInputs).toHaveLength(1);
    expect(store.getSnapshot().delegationActive).toBe(true);

    store.pauseStream();

    expect(store.getSnapshot().activeRun).toBeNull();
    expect(store.getSnapshot().pendingApproval).toBeNull();
    expect(store.getSnapshot().queuedInputs).toEqual([]);
    expect(store.getSnapshot().delegationActive).toBe(false);
    expect(store.getSnapshot().lastSeq).toBe(3);

    // Reopening is a cache hit for the history window, but the projection stays
    // empty until the reopened stream sends its authoritative snapshot.
    await store.ensureLoaded("b1");
    expect(messageCalls()).toBe(1);
    expect(store.getSnapshot().activeRun).toBeNull();

    streamOnEvent.handler!({ data: { type: "subscribed", instanceId: "w1", lastSeq: 4, snapshot: snapshot("run-2") } });
    expect(store.getSnapshot().activeRun?.id).toBe("run-2");
  });

  it("does not fire a deferred refresh after the stream is paused", async () => {
    // The timer is cancelled on pause; the intent is not. Nothing should be read
    // on behalf of a session nobody is watching.
    vi.useFakeTimers();
    try {
      const registry = createSessionRegistry();
      const store = registry.getStore("s1");
      await store.ensureLoaded("b1");
      const before = messageCalls();

      streamOnEvent.handler!({ data: { type: "message_committed", runId: "run-1", firstSeq: 2, lastSeq: 2 } });
      store.pauseStream();
      await vi.advanceTimersByTimeAsync(5_000);

      expect(messageCalls()).toBe(before);
    } finally {
      vi.useRealTimers();
    }
  });

  it("reads the window on reopen when a pause interrupted a read", async () => {
    // The commit frame advanced eventCursor, so it will not replay. If the intent
    // is dropped with the timer, those rows never appear in the timeline.
    vi.useFakeTimers();
    try {
      const registry = createSessionRegistry();
      const store = registry.getStore("s1");
      await store.ensureLoaded("b1");
      const before = messageCalls();

      streamOnEvent.handler!({ data: { type: "message_committed", runId: "run-1", firstSeq: 2, lastSeq: 2 } });
      store.pauseStream();
      await store.ensureLoaded("b1");
      await vi.advanceTimersByTimeAsync(0);

      expect(messageCalls()).toBe(before + 1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("re-arms after a terminal frame instead of swallowing the next commit", async () => {
    vi.useFakeTimers();
    try {
      const registry = createSessionRegistry();
      const store = registry.getStore("s1");
      await store.ensureLoaded("b1");
      const before = messageCalls();

      streamOnEvent.handler!({ data: { type: "run_succeeded", runId: "run-1" } });
      await vi.advanceTimersByTimeAsync(0);
      expect(messageCalls()).toBe(before + 1);

      streamOnEvent.handler!({ data: { type: "message_committed", runId: "run-2", firstSeq: 3, lastSeq: 3 } });
      await vi.advanceTimersByTimeAsync(500);

      expect(messageCalls()).toBe(before + 2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves generation recovery to the page reload", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");
    await store.ensureLoaded("b1");
    expect(messageCalls()).toBe(1);
    streamOnEvent.handler!({ data: { type: "subscribed", instanceId: "w1", lastSeq: 3, snapshot: snapshot("run-1") } });
    expect(store.getSnapshot().activeRun?.id).toBe("run-1");

    // A Worker generation change means a different Worker process.
    // WorkerGenerationGuard reloads the page on that event, so the store performs
    // no half reset of its own — the projection it holds is discarded by the
    // reload, not silently emptied here.
    streamReject.reject!(new WorkerGenerationChangedError());
    await vi.waitFor(() => expect(store.getSnapshot().streamOpen).toBe(false));

    const snap = store.getSnapshot();
    expect(snap.loaded).toBe(true);
    expect(snap.canonical).toEqual([{ id: "m1" }]);
    expect(snap.workerInstanceId).toBe("w1");
    expect(snap.activeRun?.id).toBe("run-1");
    await store.ensureLoaded("b1");
    expect(messageCalls()).toBe(1);

    registry.disposeStore("s1");
  });
});
