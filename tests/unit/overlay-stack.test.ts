import { describe, expect, it } from "vitest";

import {
  createOverlayStack,
  getOverlayStack,
  resetOverlayStackForTesting,
  type KeyboardEventLike,
  type OverlayTarget,
} from "../../lib/overlay-stack";

function fakeTarget() {
  const listeners = new Set<(event: KeyboardEventLike) => void>();
  const target: OverlayTarget & { dispatch: (event: KeyboardEventLike) => void; listeners: number } = {
    addEventListener(_type, listener) {
      listeners.add(listener);
    },
    removeEventListener(_type, listener) {
      listeners.delete(listener);
    },
    dispatch(event) {
      for (const listener of [...listeners]) listener(event);
    },
    get listeners() {
      return listeners.size;
    },
  };
  return target;
}

function escapeEvent(overrides: Partial<KeyboardEventLike> = {}) {
  const event = {
    key: "Escape",
    defaultPrevented: false,
    isComposing: false,
    prevented: false,
    preventDefault() {
      this.prevented = true;
    },
    ...overrides,
  };
  return event;
}

describe("overlay stack", () => {
  it("dismisses only the top layer, newest first", () => {
    const target = fakeTarget();
    const stack = createOverlayStack(target);
    const calls: string[] = [];

    let releaseA: () => void = () => undefined;
    let releaseB: () => void = () => undefined;
    let releaseC: () => void = () => undefined;
    releaseA = stack.push({ id: "a", onDismiss: () => { calls.push("a"); releaseA(); } });
    releaseB = stack.push({ id: "b", onDismiss: () => { calls.push("b"); releaseB(); } });
    releaseC = stack.push({ id: "c", onDismiss: () => { calls.push("c"); releaseC(); } });

    target.dispatch(escapeEvent());
    expect(calls).toEqual(["c"]);
    target.dispatch(escapeEvent());
    target.dispatch(escapeEvent());
    expect(calls).toEqual(["c", "b", "a"]);
    expect(target.listeners).toBe(0);
  });

  it("ignores Escape already consumed by an inner surface", () => {
    const target = fakeTarget();
    const stack = createOverlayStack(target);
    let dismissed = 0;
    stack.push({ id: "settings", onDismiss: () => { dismissed += 1; } });

    target.dispatch(escapeEvent({ defaultPrevented: true }));
    expect(dismissed).toBe(0);
  });

  it("ignores Escape while an IME composition is open", () => {
    const target = fakeTarget();
    const stack = createOverlayStack(target);
    let dismissed = 0;
    stack.push({ id: "dialog", onDismiss: () => { dismissed += 1; } });

    target.dispatch(escapeEvent({ isComposing: true }));
    expect(dismissed).toBe(0);
  });

  it("keeps a blocking top layer open without dismissing the layer below", () => {
    const target = fakeTarget();
    const stack = createOverlayStack(target);
    const calls: string[] = [];
    stack.push({ id: "below", onDismiss: () => calls.push("below") });
    stack.push({ id: "blocking", dismissOnEscape: false, onDismiss: () => calls.push("blocking") });

    target.dispatch(escapeEvent());
    expect(calls).toEqual([]);
  });

  it("releases idempotently and detaches the listener", () => {
    const target = fakeTarget();
    const stack = createOverlayStack(target);
    const release = stack.push({ id: "one", onDismiss: () => undefined });
    expect(target.listeners).toBe(1);
    release();
    release();
    expect(stack.size()).toBe(0);
    expect(target.listeners).toBe(0);
  });

  it("reset clears layers and removes the listener", () => {
    const target = fakeTarget();
    const stack = createOverlayStack(target);
    stack.push({ id: "a", onDismiss: () => undefined });
    stack.push({ id: "b", onDismiss: () => undefined });
    stack.reset();
    expect(stack.size()).toBe(0);
    expect(target.listeners).toBe(0);
  });
});

describe("default overlay stack", () => {
  it("is safe outside the browser", () => {
    resetOverlayStackForTesting();
    const stack = getOverlayStack();
    expect(stack.size()).toBe(0);
    expect(() => stack.push({ id: "ssr", onDismiss: () => undefined })()).not.toThrow();
    resetOverlayStackForTesting();
  });
});
