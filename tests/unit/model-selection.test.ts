import { describe, expect, it } from "vitest";
import {
  effortDisplayName,
  groupModelsByProvider,
  modelHasReasoning,
  reasoningEfforts,
} from "@/lib/model-selection";
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

describe("model-selection mapping", () => {
  it("hides the Effort row when the model does not support thinking", () => {
    const plain = model({ id: "m1", providerId: "deepseek", modelName: "deepseek-chat" });
    expect(modelHasReasoning(plain)).toBe(false);
    expect(reasoningEfforts(plain)).toEqual([]);
  });

  it("exposes catalog efforts for a thinking model, defaulting the list to [default]", () => {
    const reasoner = model({
      id: "m2",
      providerId: "deepseek",
      modelName: "deepseek-reasoner",
      supportsThinking: true,
      thinkingDialect: "openai_reasoning_effort",
      supportedThinkingEfforts: ["default", "low", "medium", "high"],
    });
    expect(modelHasReasoning(reasoner)).toBe(true);
    expect(reasoningEfforts(reasoner)).toEqual(["default", "low", "medium", "high"]);
    expect(reasoningEfforts(model({
      id: "m3", providerId: "p", modelName: "x", supportsThinking: true, supportedThinkingEfforts: [],
    }))).toEqual(["default"]);
  });

  it("capitalizes effort ids the way dsh renders Host effort.name", () => {
    expect(effortDisplayName("default")).toBe("Default");
    expect(effortDisplayName("high")).toBe("High");
  });

  it("groups models under the provider display name, not the raw id", () => {
    const groups = groupModelsByProvider(
      [
        model({ id: "a", providerId: "deepseek", modelName: "chat", displayName: "DeepSeek Chat" }),
        model({ id: "b", providerId: "deepseek", modelName: "reasoner", displayName: "DeepSeek Reasoner" }),
        model({ id: "c", providerId: "openai", modelName: "gpt-4o", displayName: "GPT-4o" }),
      ],
      [
        { id: "deepseek", name: "DeepSeek" },
        { id: "openai", name: "OpenAI" },
      ],
    );
    expect(groups.map((group) => group.name)).toEqual(["DeepSeek", "OpenAI"]);
    expect(groups[0]?.models.map((item) => item.id)).toEqual(["a", "b"]);
  });
});
