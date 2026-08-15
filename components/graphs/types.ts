import type { ModelProfile, ProviderProfile } from "@/components/settings/types";

export type ThinkingEffort = "default" | "low" | "medium" | "high";

export interface GraphTask {
  name: string;
  role?: string;
  model?: string;
  thinking?: ThinkingEffort;
  skills?: string[];
  goal: string;
  writes?: string[];
  budget?: { tokens?: number };
}

export interface GraphDocument {
  schemaVersion: number;
  id: string;
  name: string;
  description?: string;
  tasks: Record<string, GraphTask>;
  graph: Record<string, string[]>;
}

export interface GraphSummary {
  id: string;
  name: string;
  path: string;
  digest?: string;
  error?: string;
  latestVersion?: number;
}

export interface GraphDetail {
  id: string;
  name: string;
  path: string;
  digest: string;
  latestVersion: number;
  document: GraphDocument;
}

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
