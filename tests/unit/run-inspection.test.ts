import { describe, expect, it } from "vitest";
import { formatBytes, latestRunId, shortDigest } from "@/lib/run-inspection";
import type { ConversationNode, ConversationTurn } from "@/lib/chat-messages";

function turn(id: string, runId?: string): ConversationTurn {
  return { kind: "turn", id, steps: [], messageIds: [id], ...(runId ? { runId } : {}) };
}

describe("latestRunId", () => {
  it("returns the newest Run in the timeline", () => {
    const nodes: ConversationNode[] = [turn("t1", "run-1"), turn("t2", "run-2"), turn("t3", "run-3")];
    expect(latestRunId(nodes)).toBe("run-3");
  });

  it("skips a newest Turn that has no Run, such as a pending user message", () => {
    const nodes: ConversationNode[] = [turn("t1", "run-1"), turn("t2")];
    expect(latestRunId(nodes)).toBe("run-1");
  });

  it("ignores checkpoint nodes so compaction never hides the last Run", () => {
    const nodes: ConversationNode[] = [
      turn("t1", "run-1"),
      { kind: "checkpoint", id: "c1", reason: "threshold", summary: "s", reclaimedTokens: 1,
        createdAt: "2026-01-01T00:00:00Z", promptVersion: "2026-07-28", summaryContractDigest: "sha256:contract" },
    ];
    expect(latestRunId(nodes)).toBe("run-1");
  });

  it("returns null when no Turn carries a Run", () => {
    expect(latestRunId([])).toBeNull();
    expect(latestRunId([turn("t1")])).toBeNull();
  });
});

describe("shortDigest", () => {
  it("truncates a long digest and leaves a short one intact", () => {
    expect(shortDigest("0123456789abcdef", 8)).toBe("01234567…");
    expect(shortDigest("abc")).toBe("abc");
    expect(shortDigest("")).toBe("");
  });
});

describe("formatBytes", () => {
  it("reports exact bytes below a KiB and scaled units above", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2.0 KiB");
    expect(formatBytes(3 * 1024 * 1024)).toBe("3.0 MiB");
  });
});
