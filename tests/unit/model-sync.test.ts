import { describe, expect, it } from "vitest";

import { planModelSync, type BeforeModel } from "../../lib/model-sync";

const before: BeforeModel[] = [
  { id: "p/a", modelName: "a", displayName: "A", contextWindow: 1000, maxOutputTokens: 500 },
  { id: "p/b", modelName: "b", displayName: "b", contextWindow: 2000, maxOutputTokens: 1000 },
];

describe("planModelSync", () => {
  it("creates drafted models that do not exist and deletes dropped ones", () => {
    const plan = planModelSync("p", before, [
      { id: "a", name: "A", contextWindow: 1000, maxTokens: 500 },
      { id: "c", name: "C", contextWindow: 3000 },
    ]);
    expect(plan.toCreate).toEqual([
      { providerId: "p", modelName: "c", displayName: "C", contextWindow: 3000, maxOutputTokens: 16384, supportsToolUse: true },
    ]);
    expect(plan.toDelete).toEqual(["p/b"]);
    expect(plan.toUpdate).toEqual([]);
  });

  it("updates changed existing models in place", () => {
    const plan = planModelSync("p", before, [
      { id: "a", name: "A", contextWindow: 1500, maxTokens: 500 },
      { id: "b", name: "B", contextWindow: 2000, maxTokens: 8000 },
    ]);
    expect(plan.toCreate).toEqual([]);
    expect(plan.toDelete).toEqual([]);
    expect(plan.toUpdate).toEqual([
      { id: "p/a", input: { displayName: "A", contextWindow: 1500, maxOutputTokens: 500 } },
      { id: "p/b", input: { displayName: "B", contextWindow: 2000, maxOutputTokens: 8000 } },
    ]);
  });

  it("clears a custom display name back to the model id", () => {
    // `a` has a custom name "A"; dropping the name clears it to "a".
    const plan = planModelSync("p", before, [
      { id: "a", contextWindow: 1000, maxTokens: 500 },
      { id: "b", contextWindow: 2000, maxTokens: 1000 },
    ]);
    expect(plan.toUpdate).toEqual([
      { id: "p/a", input: { displayName: null, contextWindow: 1000, maxOutputTokens: 500 } },
    ]);
    expect(plan.toDelete).toEqual([]);
    expect(plan.toCreate).toEqual([]);
  });

  it("keeps unchanged models untouched", () => {
    const plan = planModelSync("p", before, [
      { id: "a", name: "A", contextWindow: 1000, maxTokens: 500 },
      { id: "b", contextWindow: 2000, maxTokens: 1000 },
    ]);
    expect(plan).toEqual({ toCreate: [], toDelete: [], toUpdate: [] });
  });

  it("ignores drafts with empty ids", () => {
    const plan = planModelSync("p", [], [{ id: "" }, { id: "  " }]);
    expect(plan.toCreate).toEqual([]);
  });
});
