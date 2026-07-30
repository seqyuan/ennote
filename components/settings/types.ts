import type { components } from "@/lib/worker-api.gen";

export type ProviderProfile = components["schemas"]["ProviderProfile"];
export type ProviderDiagnostic = components["schemas"]["ProviderDiagnostic"];
export type ModelProfile = components["schemas"]["ModelProfile"];
export type PolicyProfile = components["schemas"]["PolicyProfile"];
export type Session = components["schemas"]["Session"];

export type SettingsTab = "providers" | "models" | "policies" | "context";
