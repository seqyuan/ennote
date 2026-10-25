import { describe, expect, it } from "vitest";

import { applyTransient, projectBase, type CanonicalMessage, type ConversationNode, type TurnMessage } from "../../lib/chat-messages";
import { shouldExpandBatch, toolBatchKey } from "../../lib/tool-batch-state";

function message(id: string, role: CanonicalMessage["role"], parts: CanonicalMessage["parts"]): CanonicalMessage {
  return {
    id,
    sessionId: "session",
    role,
    status: "complete",
    speakerKind: role === "user" ? "user" : "host",
    speakerSnapshot: role === "user" ? { kind: "user", displayName: "You" } : { kind: "host", displayName: "Host" },
    visibility: "public",
    parts,
    createdAt: "2026-07-28T00:00:00Z",
  };
}

function firstBatch(node: ConversationNode) {
  if (node.kind !== "turn") throw new Error("expected turn");
  const batch = node.steps.find((step) => step.kind === "tool_batch");
  if (!batch || batch.kind !== "tool_batch") throw new Error("expected batch");
  return batch;
}

describe("tool batch state", () => {
  it("keeps the same key across transient and canonical projections", () => {
    const base = projectBase([
      message("m1", "user", [{ type: "text", text: "Inspect" }]),
      message("m2", "assistant", [{ type: "tool_call", toolCall: { id: "read-call", name: "read", arguments: {} } }]),
      message("m3", "tool", [{ type: "tool_result", toolResult: { toolCallId: "read-call", toolName: "read", content: "done", isError: false } }]),
    ], [], new Map());
    const canonical = applyTransient(base, []);
    const live: TurnMessage[] = [
      { id: "live-tool-row", role: "tool", kind: "tool", toolCallId: "read-call", toolName: "read", text: "done", isError: false, toolState: "completed" },
    ];
    const transient = applyTransient(base, live);
    expect(toolBatchKey(firstBatch(canonical[0]))).toBe("read-call");
    expect(toolBatchKey(firstBatch(transient[0]))).toBe("read-call");
  });

  it("uses the first tool call as the key when one assistant message opens several calls", () => {
    const canonical = applyTransient(projectBase([
      message("m1", "user", [{ type: "text", text: "Inspect" }]),
      message("m2", "assistant", [
        { type: "tool_call", toolCall: { id: "read-call", name: "read", arguments: {} } },
        { type: "tool_call", toolCall: { id: "grep-call", name: "grep", arguments: {} } },
      ]),
    ], [], new Map()), []);
    const batch = firstBatch(canonical[0]);
    expect(batch.activities).toHaveLength(2);
    expect(toolBatchKey(batch)).toBe("read-call");
  });

  it("expands the last, active, or approval-bearing batch by default", () => {
    expect(shouldExpandBatch({ isLast: false, hasActive: false, hasApproval: false })).toBe(false);
    expect(shouldExpandBatch({ isLast: true, hasActive: false, hasApproval: false })).toBe(true);
    expect(shouldExpandBatch({ isLast: false, hasActive: true, hasApproval: false })).toBe(true);
    expect(shouldExpandBatch({ isLast: false, hasActive: false, hasApproval: true })).toBe(true);
  });
});
