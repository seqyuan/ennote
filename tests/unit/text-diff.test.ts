import { describe, expect, it } from "vitest";

import { diffText } from "@/lib/text-diff";

describe("diffText", () => {
  it("marks removed, added, and unchanged lines", () => {
    const diff = diffText("a: 1\nb: 2\nc: 3", "a: 1\nb: 9\nc: 3\nd: 4");
    expect(diff).toContain("  a: 1");
    expect(diff).toContain("- b: 2");
    expect(diff).toContain("+ b: 9");
    expect(diff).toContain("  c: 3");
    expect(diff).toContain("+ d: 4");
  });

  it("reports identical text as all unchanged", () => {
    expect(diffText("x\ny", "x\ny")).toEqual(["  x", "  y"]);
  });

  it("reports an added tail without inventing removals", () => {
    expect(diffText("x", "x\ny")).toEqual(["  x", "+ y"]);
  });
});
