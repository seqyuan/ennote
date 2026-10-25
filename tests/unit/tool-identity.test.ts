import { describe, expect, it } from "vitest";

import { toolCallIdOf } from "../../lib/tool-identity";

describe("tool call identity", () => {
  it("prefers the explicit toolCallId", () => {
    expect(toolCallIdOf({ toolCallId: "call-1", id: "ignored", recordId: "ignored" })).toBe("call-1");
  });

  it("falls back to id, then recordId", () => {
    expect(toolCallIdOf({ id: "message-1", recordId: "record-1" })).toBe("message-1");
    expect(toolCallIdOf({ recordId: "record-1" })).toBe("record-1");
  });

  it("uses a stable event key when no identity is present", () => {
    expect(toolCallIdOf({ callIndex: 3 })).toBe("event-3");
    expect(toolCallIdOf({})).toBe("event-unknown");
  });

  it("keeps an empty explicit id rather than inventing one", () => {
    expect(toolCallIdOf({ toolCallId: "" })).toBe("");
  });
});
