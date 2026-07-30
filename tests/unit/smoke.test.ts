import { describe, it, expect } from "vitest";

describe("smoke", () => {
  it("vitest is configured", () => {
    expect(1 + 1).toBe(2);
  });

  it("node environment is correct", () => {
    expect(typeof process.env.NODE_ENV).toBe("string");
  });
});
