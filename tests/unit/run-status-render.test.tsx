import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CLOCK_SKEW_TOLERANCE_MS,
  RunStatus,
  displayedElapsed,
  elapsedOrigin,
  elapsedSince,
} from "@/components/RunStatus";
import { encodeRunStatus } from "@/lib/run-status-copy";

// RunStatus keeps its timer in an effect, and this repo's unit environment is
// node + react-dom/server (no jsdom, so no effect/paint loop). The timer rules
// are therefore pinned through the exported pure helpers the component and its
// hook use, plus a first-paint render check that nothing bogus is ever printed.
function props(overrides: Partial<Parameters<typeof RunStatus>[0]> = {}) {
  return {
    status: "running",
    active: true,
    waiting: false,
    reconnecting: false,
    compacting: false,
    runId: "run-a",
    startedAt: "2026-08-04T00:00:00Z",
    ...overrides,
  };
}

describe("RunStatus elapsed timer", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("does not show the previous run's elapsed value for a new run", () => {
    const sample = { runId: "run-a", seconds: 42 };
    expect(displayedElapsed(true, "run-a", sample)).toBe(42);
    // The component stays mounted across runs: the stale sample must not leak.
    expect(displayedElapsed(true, "run-b", sample)).toBeNull();
    // Inactive runs show no timer at all.
    expect(displayedElapsed(false, "run-a", sample)).toBeNull();
  });

  it("clears a clock-skewed (future) start time instead of pinning 0s", () => {
    vi.useFakeTimers();
    const now = Date.parse("2026-08-04T00:00:00Z");
    vi.setSystemTime(now);
    const skewed = "2026-08-04T00:01:00Z"; // Worker clock one minute ahead.
    expect(Date.parse(skewed) - Date.now()).toBeGreaterThan(CLOCK_SKEW_TOLERANCE_MS);
    const origin = elapsedOrigin(skewed, Date.now());
    expect(origin).toBe(now);
    expect(elapsedSince(origin, Date.now())).toBe(0);
    // Time still advances, so the timer is not stuck at 0 for the whole run.
    vi.setSystemTime(now + 3000);
    expect(elapsedSince(origin, Date.now())).toBe(3);
  });

  it("keeps a start time inside the skew tolerance and handles garbage", () => {
    const now = Date.parse("2026-08-04T00:00:00Z");
    const slightlyAhead = new Date(now + 1000).toISOString();
    expect(elapsedOrigin(slightlyAhead, now)).toBe(now + 1000);
    expect(elapsedOrigin(undefined, now)).toBe(now);
    expect(elapsedOrigin("not-a-date", now)).toBe(now);
    const started = Date.parse("2026-08-03T23:59:50Z");
    expect(elapsedOrigin("2026-08-03T23:59:50Z", now)).toBe(started);
    expect(elapsedSince(started, now)).toBe(10);
  });

  it("renders the translated label on first paint without a stale timer", () => {
    const html = renderToStaticMarkup(
      <RunStatus {...props({ status: encodeRunStatus("running_tool", "bash"), runId: "run-b", startedAt: "2026-08-04T00:01:00Z" })} />,
    );
    expect(html).toContain("Running: bash…");
    expect(html).not.toContain("\u001f");
    // No timer has ticked yet, so no metrics block (and no absurd value).
    expect(html).not.toContain("run-status-metrics");
    expect(html).not.toContain("NaN");
  });
});
