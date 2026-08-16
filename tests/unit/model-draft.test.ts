import { describe, expect, it } from "vitest";

import { modelDrafts, validateModels } from "../../lib/model-draft";

describe("modelDrafts", () => {
  it("converts an array of objects and drops non-object entries", () => {
    expect(modelDrafts([{ id: "a" }, 3, { id: "b" }])).toEqual([{ id: "a" }, {}, { id: "b" }]);
  });

  it("returns an empty array for non-arrays", () => {
    expect(modelDrafts(undefined)).toEqual([]);
    expect(modelDrafts("nope")).toEqual([]);
  });
});

describe("validateModels", () => {
  it("returns undefined for an absent (inherited) list", () => {
    expect(validateModels(undefined)).toBeUndefined();
  });

  it("rejects an empty model id", () => {
    expect(validateModels([{ id: "" }])).toEqual({ index: 0, key: "modelIdRequired" });
    expect(validateModels([{ id: "  " }])).toEqual({ index: 0, key: "modelIdRequired" });
  });

  it("rejects a duplicate id (compared trimmed)", () => {
    expect(validateModels([{ id: "a" }, { id: "a " }])).toEqual({ index: 1, key: "modelIdDuplicate" });
  });

  it("rejects an explicit empty display name", () => {
    expect(validateModels([{ id: "a", name: "" }])).toEqual({ index: 0, key: "modelNameInvalid" });
    expect(validateModels([{ id: "a", name: 3 }])).toEqual({ index: 0, key: "modelNameInvalid" });
  });

  it("rejects non-positive or fractional capacities", () => {
    expect(validateModels([{ id: "a", contextWindow: 0 }])).toEqual({ index: 0, key: "modelContextInvalid" });
    expect(validateModels([{ id: "a", contextWindow: 1.5 }])).toEqual({ index: 0, key: "modelContextInvalid" });
    expect(validateModels([{ id: "a", maxTokens: -1 }])).toEqual({ index: 0, key: "modelMaxTokensInvalid" });
  });

  it("accepts a valid array", () => {
    expect(validateModels([{ id: "a", name: "A", contextWindow: 256000, maxTokens: 32000 }])).toBeUndefined();
  });

  it("preserves unknown fields untouched (structurally open)", () => {
    const models = [{ id: "a", thinking: true }];
    validateModels(models);
    expect(models[0].thinking).toBe(true);
  });
});
