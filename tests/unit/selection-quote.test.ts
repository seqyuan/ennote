import { describe, expect, it } from "vitest";

import { MAX_QUOTE_CHARS, buildExplainRequest, buildQuote, normalizeSelection } from "../../lib/selection-quote";

describe("selection quote", () => {
  it("normalizes trailing whitespace and excess blank lines", () => {
    expect(normalizeSelection("  a  \n\n\n\nb  \r\n")).toBe("a\n\nb");
  });

  it("prefixes every line and records the source", () => {
    expect(buildQuote("first\nsecond", "Conversation")).toBe(
      "> first\n> second\n>\n> — Conversation",
    );
  });

  it("caps a very long selection", () => {
    const quote = buildQuote("x".repeat(MAX_QUOTE_CHARS + 200), "Conversation");
    expect(quote).not.toBeNull();
    expect(quote!.length).toBeLessThan(MAX_QUOTE_CHARS + 40);
    expect(quote!.endsWith("…\n>\n> — Conversation")).toBe(true);
  });

  it("returns null for an empty selection", () => {
    expect(buildQuote("   \n\t ", "Conversation")).toBeNull();
  });

  it("keeps the explain prompt ahead of the quote", () => {
    const quote = buildQuote("text", "Conversation")!;
    expect(buildExplainRequest("Explain this:", quote)).toBe(`Explain this:\n\n${quote}`);
  });
});
