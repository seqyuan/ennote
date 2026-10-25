import { describe, expect, it } from "vitest";

import { moveTab } from "../../lib/tab-order";

const tabs = [{ id: "a" }, { id: "b" }, { id: "c" }, { id: "d" }];

describe("tab order", () => {
  it("moves a tab forward to the target position", () => {
    expect(moveTab(tabs, "a", "c").map((tab) => tab.id)).toEqual(["b", "c", "a", "d"]);
  });

  it("moves a tab backward", () => {
    expect(moveTab(tabs, "d", "b").map((tab) => tab.id)).toEqual(["a", "d", "b", "c"]);
  });

  it("ignores unknown or identical ids", () => {
    expect(moveTab(tabs, "a", "a")).toBe(tabs);
    expect(moveTab(tabs, "missing", "a")).toBe(tabs);
  });
});
