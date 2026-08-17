import { describe, expect, it } from "vitest";

import {
  applyTransient,
  mergeTimeline,
  prependCanonicalMessages,
  projectBase,
  reconcileLatestMessages,
  type CanonicalMessage,
  type ContextCheckpoint,
  type TurnMessage,
} from "../../lib/chat-messages";

function message(
  id: string,
  role: CanonicalMessage["role"],
  text: string,
  parts?: CanonicalMessage["parts"],
  visibility: CanonicalMessage["visibility"] = role === "tool" ? "legacy_execution" : "public",
): CanonicalMessage {
  return {
    id,
    sessionId: "session",
    role,
    status: "complete",
    speakerKind: role === "user" ? "user" : "host",
    speakerSnapshot: role === "user"
      ? { kind: "user", displayName: "You" }
      : { kind: "host", displayName: "Host" },
    addresseeKind: role === "user" ? "host" : undefined,
    visibility,
    parts: parts ?? [{ type: "text", text }],
    createdAt: `2026-07-28T00:00:0${id.slice(-1)}Z`,
  };
}

describe("structured conversation projection", () => {
  it("groups a user request, tool batch, results, and continuation into one turn", () => {
    const timeline = mergeTimeline([
      message("m1", "user", "Inspect the data"),
      message("m2", "assistant", "", [
        { type: "thinking", text: "I should inspect both files." },
        { type: "text", text: "I will inspect the inputs." },
        { type: "tool_call", toolCall: { id: "read-call", name: "read", arguments: { path: "/workspace/a.csv" } } },
        { type: "tool_call", toolCall: { id: "grep-call", name: "grep", arguments: { query: "sample", path: "/workspace" } } },
      ]),
      message("m3", "tool", "", [
        { type: "tool_result", toolResult: { toolCallId: "grep-call", toolName: "grep", content: "b.csv:sample", isError: false } },
      ]),
      message("m4", "tool", "", [
        { type: "tool_result", toolResult: { toolCallId: "read-call", toolName: "read", content: "header", isError: false } },
      ]),
      message("m5", "assistant", "Both inputs are valid."),
    ], [], []);

    expect(timeline).toHaveLength(1);
    const turn = timeline[0];
    expect(turn.kind).toBe("turn");
    if (turn.kind !== "turn") return;
    expect(turn.user?.text).toBe("Inspect the data");
    expect(turn.steps.map(step => step.kind)).toEqual(["assistant", "tool_batch", "assistant"]);
    const batch = turn.steps[1];
    expect(batch.kind).toBe("tool_batch");
    if (batch.kind !== "tool_batch") return;
    expect(batch.activities.map(activity => activity.toolCallId)).toEqual(["read-call", "grep-call"]);
    expect(batch.activities[0].result?.content).toBe("header");
    expect(batch.activities[1].result?.content).toBe("b.csv:sample");
  });

  it("preserves ordered typed artifact references on their tool activity", () => {
    const artifacts = [
      { artifactId: "table-1", name: "results.csv", kind: "table" as const, mimeType: "text/csv; charset=utf-8", sizeBytes: 42, sha256: "abc" },
      { artifactId: "plot-1", name: "plot.png", kind: "image" as const, mimeType: "image/png", sizeBytes: 84, sha256: "def", width: 640, height: 480 },
    ];
    const timeline = mergeTimeline([
      message("m1", "user", "Analyze"),
      message("m2", "assistant", "", [
        { type: "tool_call", toolCall: { id: "publish", name: "publish_artifact", arguments: { path: "/workspace/results.csv" } } },
      ]),
      message("m3", "tool", "", [
        { type: "tool_result", toolResult: { toolCallId: "publish", toolName: "publish_artifact", content: "published", isError: false, artifacts } },
      ]),
      message("m4", "assistant", "Complete"),
    ], [], []);
    const turn = timeline[0];
    expect(turn.kind).toBe("turn");
    if (turn.kind !== "turn") return;
    const batch = turn.steps.find(step => step.kind === "tool_batch");
    expect(batch?.kind).toBe("tool_batch");
    if (!batch || batch.kind !== "tool_batch") return;
    expect(batch.activities[0].result?.artifacts).toEqual(artifacts);
    expect(batch.activities[0].riskClass).toBe("local_write");
  });

  it("keeps format-1 execution rows but excludes private transcript rows", () => {
    const timeline = mergeTimeline([
      message("m1", "user", "Inspect"),
      message("m2", "assistant", "working", undefined, "legacy_execution"),
      message("m3", "assistant", "private reasoning", undefined, "private"),
      message("m4", "assistant", "Done"),
    ], [], []);

    expect(timeline).toHaveLength(1);
    const turn = timeline[0];
    expect(turn.kind).toBe("turn");
    if (turn.kind !== "turn") return;
    expect(turn.messageIds).toEqual(["m1", "m2", "m4"]);
    expect(turn.steps).toHaveLength(2);
  });

  it("keeps steer input and orphan tool results instead of dropping them", () => {
    const transient: TurnMessage[] = [
      { id: "u1", role: "user", text: "Start" },
      { id: "s1", role: "user", kind: "steer", text: "Use CSV instead" },
      { id: "t1", role: "tool", kind: "tool", toolCallId: "orphan", toolName: "read", text: "done", isError: false },
    ];
    const timeline = mergeTimeline([], [], transient);
    const turn = timeline[0];
    expect(turn.kind).toBe("turn");
    if (turn.kind !== "turn") return;
    expect(turn.steps.map(step => step.kind)).toEqual(["steer", "tool_batch"]);
    const batch = turn.steps[1];
    if (batch.kind !== "tool_batch") return;
    expect(batch.activities[0]).toMatchObject({ toolCallId: "orphan", state: "completed" });
  });

  it("places a completed checkpoint before its first kept message and only once", () => {
    const checkpoint: ContextCheckpoint = {
      id: "checkpoint",
      status: "completed",
      reason: "manual",
      summary: "state summary",
      reclaimedTokens: 100,
      firstKeptMessageId: "m3",
      sourceThroughMessageId: "m2",
      baseLeafMessageId: "m4",
      createdAt: "2026-07-28T00:00:05Z",
    };
    const timeline = mergeTimeline(
      [message("m1", "user", "one"), message("m2", "assistant", "two"), message("m3", "user", "three"), message("m4", "assistant", "four")],
      [checkpoint, checkpoint],
      [],
    );
    expect(timeline.map(item => item.id)).toEqual(["turn-m1", "compaction-checkpoint", "turn-m3"]);
    expect(timeline[1]).toMatchObject({ kind: "checkpoint", summary: "state summary", reclaimedTokens: 100 });
  });
});

describe("split projection (projectBase + applyTransient)", () => {
  const completedTurn = (): CanonicalMessage[] => [
    message("m1", "user", "Inspect"),
    message("m2", "assistant", "", [
      { type: "text", text: "Inspecting…" },
      { type: "tool_call", toolCall: { id: "c1", name: "read", arguments: { path: "/a" } } },
    ]),
    message("m3", "tool", "", [
      { type: "tool_result", toolResult: { toolCallId: "c1", toolName: "read", content: "ok", isError: false } },
    ]),
    message("m4", "assistant", "Done"),
  ];
  const streamingTail = (): TurnMessage[] => [
    { id: "u2", role: "user", text: "Next" },
    { id: "a2", role: "assistant", text: "streaming" },
    { id: "t2", role: "tool", kind: "tool", toolCallId: "c2", toolName: "grep", text: "hit", isError: false },
  ];

  it("is equivalent to mergeTimeline across canonical/transient/checkpoint mixes", () => {
    const checkpoint: ContextCheckpoint = {
      id: "cp", status: "completed", reason: "manual", summary: "s", reclaimedTokens: 1,
      firstKeptMessageId: "m1", sourceThroughMessageId: "m1", baseLeafMessageId: "m4", createdAt: "2026-07-28T00:00:05Z",
    };
    const cases = [
      { messages: completedTurn(), checkpoints: [] as ContextCheckpoint[], transient: [] as TurnMessage[] },
      { messages: completedTurn(), checkpoints: [] as ContextCheckpoint[], transient: streamingTail() },
      { messages: completedTurn(), checkpoints: [checkpoint], transient: streamingTail() },
      { messages: [] as CanonicalMessage[], checkpoints: [] as ContextCheckpoint[], transient: streamingTail() },
    ];
    for (const c of cases) {
      expect(applyTransient(projectBase(c.messages, c.checkpoints), c.transient))
        .toEqual(mergeTimeline(c.messages, c.checkpoints, c.transient));
    }
  });

  const twoTurns = (): CanonicalMessage[] => [
    message("m1", "user", "First"),
    message("m2", "assistant", "First reply"),
    message("m3", "user", "Second"),
    message("m4", "assistant", "Second reply"),
  ];

  it("preserves base node references and returns base.nodes when there is no open turn", () => {
    const base = projectBase(twoTurns(), []);
    expect(base.nodes).toHaveLength(1);
    const out = applyTransient(base, streamingTail());
    expect(out.length).toBeGreaterThan(base.nodes.length);
    for (let i = 0; i < base.nodes.length; i += 1) expect(out[i]).toBe(base.nodes[i]);

    // No open turn (empty canonical): the empty-transient fast path returns base.nodes.
    const emptyBase = projectBase([], []);
    expect(applyTransient(emptyBase, [])).toBe(emptyBase.nodes);
  });

  it("finalizes the open turn on empty transient without mutating base", () => {
    const base = projectBase(twoTurns(), []);
    const openTurnRef = base.openTurn;
    const out = applyTransient(base, []);
    expect(out).toHaveLength(2);
    expect(out[0]).toBe(base.nodes[0]);
    expect(out[1]).not.toBe(openTurnRef); // finalized clone, not the base object
    expect(openTurnRef?.metrics).toBeUndefined(); // base.openTurn untouched
  });

  it("does not mutate base when a streaming tool delta merges into a canonical tool call", () => {
    const base = projectBase([
      message("m1", "user", "go"),
      message("m2", "assistant", "", [
        { type: "tool_call", toolCall: { id: "c1", name: "read", arguments: {} } },
      ]),
    ], []);
    const baseBatch = base.openTurn?.steps.find(step => step.kind === "tool_batch");
    expect(baseBatch?.kind).toBe("tool_batch");
    if (baseBatch?.kind !== "tool_batch") return;
    expect(baseBatch.activities[0].result).toBeUndefined();

    applyTransient(base, [
      { id: "t1", role: "tool", kind: "tool", toolCallId: "c1", toolName: "read", text: "result", isError: false },
    ]);

    // base.openTurn's activity is untouched (clone-on-write isolated the merge).
    expect(baseBatch.activities[0].result).toBeUndefined();
  });
});

describe("canonical history reconciliation", () => {
  it("prepends pages and removes overlapping message ids", () => {
    const current = [message("m3", "user", "three"), message("m4", "assistant", "four")];
    const older = [message("m1", "user", "one"), message("m2", "assistant", "two"), message("m3", "user", "three")];
    expect(prependCanonicalMessages(current, older).map(item => item.id)).toEqual(["m1", "m2", "m3", "m4"]);
  });

  it("reconciles the latest page while preserving loaded ancestors", () => {
    const current = [message("m1", "user", "one"), message("m2", "assistant", "two"), message("m3", "user", "three")];
    const latest = [message("m2", "assistant", "two updated"), message("m3", "user", "three"), message("m4", "assistant", "four")];
    const result = reconcileLatestMessages(current, latest);
    expect(result.map(item => item.id)).toEqual(["m1", "m2", "m3", "m4"]);
    expect(result[1].parts[0]).toEqual({ type: "text", text: "two updated" });
  });
});
