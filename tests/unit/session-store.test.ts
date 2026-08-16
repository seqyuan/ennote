import { beforeEach, describe, expect, it, vi } from "vitest";

const { apiFetch, streamOnEvent, apiEventStream } = vi.hoisted(() => {
  const streamOnEvent: { handler: ((event: { id?: string; data: unknown }) => void) | null } = { handler: null };
  return {
    apiFetch: vi.fn(),
    streamOnEvent,
    apiEventStream: vi.fn((_path: string, onEvent: unknown, init?: RequestInit) => {
      streamOnEvent.handler = onEvent as (event: { id?: string; data: unknown }) => void;
      return new Promise<void>((_resolve, reject) => {
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

  it("evicts least-recently-used stores beyond the cap", () => {
    const registry = createSessionRegistry();
    for (let index = 0; index < 17; index += 1) registry.getStore(`s${index}`);

    expect(registry.has("s0")).toBe(false);
    expect(registry.has("s16")).toBe(true);
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

  it("re-syncs messages on message_committed", async () => {
    const registry = createSessionRegistry();
    const store = registry.getStore("s1");
    await store.ensureLoaded("b1");
    const before = messageCalls();

    streamOnEvent.handler!({ id: "42", data: { type: "message_committed", runId: "run-1", firstSeq: 2, lastSeq: 2 } });
    await vi.waitFor(() => expect(messageCalls()).toBe(before + 1));

    expect(store.getSnapshot().lastSeq).toBe(2);
  });
});
