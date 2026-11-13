import { describe, expect, it } from "vitest";
import { modelOptions } from "@/components/graphs/types";
import type { ModelProfile } from "@/components/settings/types";

function model(partial: Partial<ModelProfile> & Pick<ModelProfile, "id" | "providerId" | "modelName">): ModelProfile {
  return {
    displayName: partial.displayName ?? partial.modelName,
    contextWindow: 1,
    maxOutputTokens: 1,
    inputCostUsdMicrosPerMillion: 0,
    outputCostUsdMicrosPerMillion: 0,
    supportsVision: false,
    supportsToolUse: true,
    supportsThinking: false,
    thinkingDialect: "none",
    supportedThinkingEfforts: ["default"],
    isDefault: false,
    status: "active",
    createdAt: "",
    updatedAt: "",
    ...partial,
  };
}

describe("modelOptions", () => {
  // Regression: the Graph and Hero editors used to build the portable ref from
  // the provider's DISPLAY name, while the resolver matches ModelProfile.ID
  // built from the provider KEY. A provider whose key ("openai-main") differs
  // from its name ("OpenAI") therefore produced refs that never resolved.
  it("builds the ref from the provider key, not the display name", () => {
    const options = modelOptions([
      model({
        id: "openai-main/org/gpt-5.4",
        providerId: "openai-main",
        modelName: "org/gpt-5.4",
        displayName: "GPT-5.4",
      }),
    ]);

    expect(options).toHaveLength(1);
    expect(options[0]?.ref).toBe("openai-main/org/gpt-5.4");
    expect(options[0]?.label).toBe("GPT-5.4");
    expect(options[0]?.model.providerId).toBe("openai-main");
  });

  it("drops inactive models", () => {
    const options = modelOptions([
      model({ id: "a/p", providerId: "a", modelName: "p", status: "inactive" }),
      model({ id: "b/q", providerId: "b", modelName: "q" }),
    ]);

    expect(options.map((option) => option.ref)).toEqual(["b/q"]);
  });
});
