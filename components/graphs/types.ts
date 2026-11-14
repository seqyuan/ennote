import type { components } from "@/lib/worker-api.gen";
import type { ModelProfile } from "@/components/settings/types";

// The Graph surface reads the contract's own types. It used to restate them, and
// the copies had already drifted: `GraphSummary.latestVersion` was optional here
// while the Worker always answers it, so `tsc` could not flag a reader that
// assumed the field might be missing.
export type ThinkingEffort = components["schemas"]["ThinkingEffort"];
export type GraphSummary = components["schemas"]["GraphSummary"];
export type ImmutableFileRevision = components["schemas"]["ImmutableFileRevision"];

// The unified Graph authoring document is the Agent Flow FlowDefinition. The
// contract declares `definition` loosely (additionalProperties: true), so the
// editor restates the fields it reads and writes.
export interface FlowPort {
  type: string;
  required?: boolean;
}

export interface FlowTaskBudget {
  tokens?: number;
}

export interface FlowFanOut {
  min: number;
  max: number;
}

export interface FlowTerminal {
  status: string;
  output?: string;
}

export interface FlowTask {
  hero?: string;
  skills?: string[];
  goal?: string;
  depends?: string[];
  type?: string;
  command?: string;
  fanOut?: FlowFanOut;
  budget?: FlowTaskBudget;
  terminal?: FlowTerminal;
  output?: string;
  next?: Record<string, string>;
  writes?: string[];
}

export interface FlowConvergenceRule {
  from: string;
  to: string;
  maxRounds: number;
}

export interface FlowParallelism {
  max?: number;
  allowDisjointWriters?: boolean;
}

export interface FlowDefinition {
  schemaVersion: number;
  id: string;
  name?: string;
  version?: number;
  description?: string;
  inputs?: Record<string, FlowPort>;
  outputs?: Record<string, FlowPort>;
  budget: { maxTotalTokens: number };
  tasks: Record<string, FlowTask>;
  convergence?: FlowConvergenceRule[];
  parallelism?: FlowParallelism;
}

// GraphDetail carries the unified Graph definition.
export type GraphDetail = Omit<components["schemas"]["GraphDetail"], "definition"> & {
  definition?: FlowDefinition;
};

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
