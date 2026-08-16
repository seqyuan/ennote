import { describe, expect, it } from "vitest";

import { planModelSync, type BeforeModel } from "../../lib/model-sync";

const before: BeforeModel[] = [
  { id: "p/a", modelName: "a", displayName: "A", contextWindow: 1000, maxOutputTokens: 500 },
  { id: "p/b", modelName: "b", displayName: "B", contextWindow: 2000, maxOutputTokens: 1000, isDefault: true },
];

describe("planModelSync", () => {
  it("creates drafted models that do not exist and deletes dropped ones", () => {
    const plan = planModelSync("p", before, [{ id: "a" }, { id: "c", name: "C", contextWindow: 3000 }]);
    expect(plan.toCreate).toEqual([
      { providerId: "p", modelName: "c", displayName: "C", contextWindow: 3000, maxOutputTokens: 16384, supportsToolUse: true },
    ]);
    expect(plan.toDelete).toEqual(["p/b"]);
    expect(plan.toRecreate).toEqual([]);
  });

  it("deletes existing models removed from the draft", () => {
    const plan = planModelSync("p", before, [{ id: "a" }]);
    expect(plan.toDelete).toEqual(["p/b"]);
    expect(plan.toCreate).toEqual([]);
    expect(plan.toRecreate).toEqual([]);
  });

  it("recreates changed existing models and preserves the default flag", () => {
    const plan = planModelSync("p", before, [
      { id: "a", contextWindow: 1500 },
      { id: "b", maxTokens: 8000 },
    ]);
    expect(plan.toCreate).toEqual([]);
    expect(plan.toDelete).toEqual([]);
    expect(plan.toRecreate).toHaveLength(2);
    const changedA = plan.toRecreate.find(r => r.deleteId === "p/a");
    expect(changedA?.input.contextWindow).toBe(1500);
    expect(changedA?.wasDefault).toBe(false);
    const changedB = plan.toRecreate.find(r => r.deleteId === "p/b");
    expect(changedB?.input.maxOutputTokens).toBe(8000);
    expect(changedB?.wasDefault).toBe(true);
  });

  it("keeps unchanged models untouched and inherits capacities from the existing profile", () => {
    const plan = planModelSync("p", before, [{ id: "a" }, { id: "b" }]);
    expect(plan.toCreate).toEqual([]);
    expect(plan.toDelete).toEqual([]);
    expect(plan.toRecreate).toEqual([]);
  });

  it("ignores drafts with empty ids", () => {
    const plan = planModelSync("p", [], [{ id: "" }, { id: "  " }]);
    expect(plan.toCreate).toEqual([]);
  });
});
