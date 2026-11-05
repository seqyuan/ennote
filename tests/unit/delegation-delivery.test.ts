import { describe, expect, it } from "vitest";

import {
  planHandleRefresh,
  shouldFetchHandleDetail,
  type BackgroundDelivery,
  type DelegationHandle,
} from "@/lib/delegation-delivery";

const session = "sess-1";

function handle(overrides: Partial<DelegationHandle> = {}): DelegationHandle {
  return {
    id: "handle-1",
    groupId: "group-1",
    sessionId: session,
    sourceParentRunId: "run-1",
    sourceBranchId: "main",
    executionMode: "background",
    autoResume: true,
    status: "active",
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
    ...overrides,
  } as DelegationHandle;
}

function row(overrides: Partial<DelegationHandle> = {}, completion?: BackgroundDelivery["completion"]): BackgroundDelivery {
  const value: BackgroundDelivery = { handle: handle(overrides) };
  if (completion) value.completion = completion;
  return value;
}

describe("shouldFetchHandleDetail", () => {
  it("fetches a handle it has never seen", () => {
    expect(shouldFetchHandleDetail(undefined, handle())).toBe(true);
  });

  it("reuses a row whose updatedAt and status have not moved", () => {
    expect(shouldFetchHandleDetail(row(), handle())).toBe(false);
  });

  it("refetches when the Worker touched the handle", () => {
    expect(shouldFetchHandleDetail(row(), handle({ updatedAt: "2026-01-01T00:00:05Z" }))).toBe(true);
  });

  it("retries a completed handle whose completion never arrived", () => {
    const listed = handle({ status: "completed" });
    expect(shouldFetchHandleDetail(row({ status: "completed" }), listed)).toBe(true);
  });

  it("stops polling a completed handle once the completion is known", () => {
    const listed = handle({ status: "completed" });
    const previous = row({ status: "completed" }, { id: "completion-1" } as BackgroundDelivery["completion"]);
    expect(shouldFetchHandleDetail(previous, listed)).toBe(false);
  });
});

describe("planHandleRefresh", () => {
  it("costs no detail requests while a session is quiet", () => {
    // The N+1 this replaces: five idle handles used to mean five detail calls
    // every tick, forever.
    const items = Array.from({ length: 5 }, (_, index) => handle({ id: `handle-${index}` }));
    const previous: Record<string, BackgroundDelivery> = {};
    for (const item of items) previous[item.id] = { handle: item };

    const plan = planHandleRefresh(previous, session, items);

    expect(plan.toFetch).toHaveLength(0);
    expect(Object.keys(plan.rows)).toHaveLength(5);
  });

  it("fetches exactly the handles that moved", () => {
    const items = [handle({ id: "a" }), handle({ id: "b", updatedAt: "2026-01-01T00:00:09Z" }), handle({ id: "c" })];
    const previous: Record<string, BackgroundDelivery> = {
      a: row({ id: "a" }),
      b: row({ id: "b" }),
      c: row({ id: "c" }),
    };

    const plan = planHandleRefresh(previous, session, items);

    expect(plan.toFetch.map((item) => item.id)).toEqual(["b"]);
    expect(plan.rows.a).toBe(previous.a);
    expect(plan.rows.c).toBe(previous.c);
  });

  it("drops handles from another session instead of caching them", () => {
    const items = [handle({ id: "mine" }), handle({ id: "theirs", sessionId: "sess-2" })];

    const plan = planHandleRefresh({}, session, items);

    expect(plan.toFetch.map((item) => item.id)).toEqual(["mine"]);
    expect(plan.rows.theirs).toBeUndefined();
  });

  it("does not reuse a row cached under another session id", () => {
    const previous = { "handle-1": row({ sessionId: "sess-2" }) };

    const plan = planHandleRefresh(previous, session, [handle()]);

    expect(plan.toFetch).toHaveLength(1);
    expect(plan.rows["handle-1"]).toBeUndefined();
  });
});
