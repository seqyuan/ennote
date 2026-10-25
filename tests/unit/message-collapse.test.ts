import { describe, expect, it } from "vitest";

import { MESSAGE_COLLAPSE_CHARS, MESSAGE_COLLAPSE_LINES, shouldCollapseMessage } from "../../lib/message-collapse";

describe("message collapse", () => {
  it("keeps short messages open", () => {
    expect(shouldCollapseMessage("A short answer.")).toBe(false);
  });

  it("collapses long prose", () => {
    expect(shouldCollapseMessage("x".repeat(MESSAGE_COLLAPSE_CHARS + 1))).toBe(true);
  });

  it("collapses many short lines", () => {
    expect(shouldCollapseMessage(Array.from({ length: MESSAGE_COLLAPSE_LINES + 1 }, () => "log").join("\n"))).toBe(true);
  });

  it("accepts custom thresholds", () => {
    expect(shouldCollapseMessage("abcdef", { charLimit: 3, lineLimit: 99 })).toBe(true);
    expect(shouldCollapseMessage("a\nb", { charLimit: 99, lineLimit: 1 })).toBe(true);
  });
});
