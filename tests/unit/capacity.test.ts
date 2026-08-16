import { describe, expect, it } from "vitest";

import { formatCapacity, parseCapacity } from "../../lib/capacity";

describe("parseCapacity", () => {
  it("parses plain counts", () => {
    expect(parseCapacity("131072")).toBe(131072);
    expect(parseCapacity("8192")).toBe(8192);
  });

  it("parses K and M suffixes (K = 1000)", () => {
    expect(parseCapacity("256K")).toBe(256000);
    expect(parseCapacity("1M")).toBe(1000000);
    expect(parseCapacity("1.5k")).toBe(1500);
  });

  it("returns undefined for blank input", () => {
    expect(parseCapacity("")).toBeUndefined();
    expect(parseCapacity("   ")).toBeUndefined();
  });

  it("returns NaN for unreadable text", () => {
    expect(Number.isNaN(parseCapacity("abc"))).toBe(true);
    expect(Number.isNaN(parseCapacity("12X"))).toBe(true);
  });
});

describe("formatCapacity", () => {
  it("spells the shortest round-trippable form", () => {
    expect(formatCapacity(256000)).toBe("256K");
    expect(formatCapacity(1000000)).toBe("1M");
    expect(formatCapacity(131072)).toBe("131072");
  });

  it("writes non-integer and non-positive values literally", () => {
    expect(formatCapacity(0)).toBe("0");
    expect(formatCapacity(-5)).toBe("-5");
  });
});
