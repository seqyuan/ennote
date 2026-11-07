import type { components } from "@/lib/worker-api.gen";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";

// The Graph surface reads the contract's own types. It used to restate them, and
// the copies had already drifted: `GraphSummary.latestVersion` was optional here
// while the Worker always answers it, so `tsc` could not flag a reader that
// assumed the field might be missing.
export type ThinkingEffort = components["schemas"]["ThinkingEffort"];
export type GraphTask = components["schemas"]["GraphTask"];
export type GraphDocument = components["schemas"]["GraphDocument"];
export type GraphSummary = components["schemas"]["GraphSummary"];
export type GraphDetail = components["schemas"]["GraphDetail"];

export interface ModelOption {
  ref: string;
  label: string;
  model: ModelProfile;
}

export function modelOptions(models: ModelProfile[], providers: ProviderProfile[]): ModelOption[] {
  const providerNames = new Map(providers.map((provider) => [provider.id, provider.name]));
  return models
    .filter((model) => model.status === "active")
    .map((model) => ({
      ref: `${providerNames.get(model.providerId) ?? model.providerId}/${model.modelName}`,
      label: model.displayName || model.modelName,
      model,
    }));
}
