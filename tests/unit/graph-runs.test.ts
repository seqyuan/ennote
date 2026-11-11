import { describe, expect, it } from "vitest";
import { nextExpandedRun } from "@/lib/graph-runs";
import type { components } from "@/lib/worker-api.gen";

type RunAgentFlow = components["schemas"]["RunAgentFlow"];

function run(runId: string, createdAt: string): RunAgentFlow {
  return {
    runId, sessionId: "s", projectId: "p", flowVersionId: "v1",
    manifestDigest: "a".repeat(64), state: "running", totalTokensUsed: 0,
    inputs: {}, createdAt, updatedAt: createdAt,
  } as RunAgentFlow;
}

const older = run("run-old", "2026-08-07T00:00:00Z");
const newer = run("run-new", "2026-08-07T00:10:00Z");

describe("nextExpandedRun", () => {
  it("expands the newest run when nothing is expanded", () => {
    expect(nextExpandedRun(null, [newer, older], null)).toBe("run-new");
  });

  it("keeps the run the reader collapsed", () => {
    // The panel polls while a run is active. Before this rule, every refresh
    // re-expanded the card the reader had just closed.
    expect(nextExpandedRun(null, [newer, older], "run-new")).toBeNull();
  });

  it("still expands a run newer than the one the reader collapsed", () => {
    const newest = run("run-newest", "2026-08-07T00:20:00Z");
    expect(nextExpandedRun(null, [newest, newer, older], "run-new")).toBe("run-newest");
  });

  it("moves to the newest run when the expanded one disappears", () => {
    expect(nextExpandedRun("run-old", [newer], null)).toBe("run-new");
  });

  it("hands the panel to a run that arrives after the one on screen", () => {
    // A newer run is live progress and outranks the older one being read; this is
    // the behaviour the panel always had, and the fix above must not change it.
    const newest = run("run-newest", "2026-08-07T00:20:00Z");
    expect(nextExpandedRun("run-old", [newest, newer, older], null)).toBe("run-newest");
  });

  it("clears the expansion when the list empties", () => {
    expect(nextExpandedRun("run-new", [], null)).toBeNull();
  });
});
