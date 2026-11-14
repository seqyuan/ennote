import type { components } from "@/lib/worker-api.gen";
import type { ModelProfile } from "@/components/settings/types";

// The Graph surface reads the contract's own types. It used to restate them, and
// the copies had already drifted: `GraphSummary.latestVersion` was optional here
// while the Worker always answers it, so `tsc` could not flag a reader that
// assumed the field might be missing.
export type ThinkingEffort = components["schemas"]["ThinkingEffort"];
export type GraphTask = components["schemas"]["GraphTask"];
export type GraphDocument = components["schemas"]["GraphDocument"];
export type GraphSummary = components["schemas"]["GraphSummary"];
// GraphDetail now carries an optional `definition` (the unified FlowDefinition)
// and keeps `document` only for a legacy graph-v1 read. Until the editor is
// switched to `definition` (graph-unification Task 8), the legacy editor still
// reads `document`, so the alias re-declares it non-optional for that consumer.
export type GraphDetail = components["schemas"]["GraphDetail"] & {
  document: components["schemas"]["GraphDocument"];
};
export type ImmutableFileRevision = components["schemas"]["ImmutableFileRevision"];

export interface ModelOption {
  ref: string;
  label: string;
  model: ModelProfile;
}

export function modelOptions(models: ModelProfile[]): ModelOption[] {
  return models
    .filter((model) => model.status === "active")
    .map((model) => ({
      ref: `${model.providerId}/${model.modelName}`,
      label: model.displayName || model.modelName,
      model,
    }));
}
