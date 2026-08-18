import { apiFetch } from "@/lib/worker-api.client";
import type { ModelDraft } from "@/lib/model-draft";

/** Default capacities for a provider created with a model whose draft carries
 *  no capacity. A hand-declared or directory-adopted route has no catalog
 *  fallback on the create path (the catalog serves only catalog-known keys
 *  with an empty model list), so a blank capacity falls back to common
 *  defaults rather than failing the write. */
export const DEFAULT_CONTEXT_WINDOW = 131072;
export const DEFAULT_MAX_TOKENS = 16384;

/** Create a provider profile and its declared models through the wire.
 *  Shared by the custom-provider card and the directory setup/adopt editors:
 *  the provider write lands first, then each model, so a model failure leaves
 *  a retryable provider row instead of an orphaned catalog. */
export async function createProviderWithModels(input: {
  key: string;
  name: string;
  providerType: string;
  baseUrl: string;
  apiKey?: string;
  models: readonly ModelDraft[];
}): Promise<void> {
  await apiFetch("/v1/provider-profiles", { method: "POST", body: JSON.stringify({
    key: input.key,
    name: input.name,
    providerType: input.providerType,
    baseUrl: input.baseUrl,
    apiKey: input.apiKey || undefined,
  }) });
  for (const model of input.models) {
    const id = String(model.id ?? "").trim();
    if (id.length === 0) continue;
    await apiFetch("/v1/model-profiles", { method: "POST", body: JSON.stringify({
      providerId: input.key,
      modelName: id,
      ...(typeof model.name === "string" && model.name.length > 0 ? { displayName: model.name } : {}),
      contextWindow: typeof model.contextWindow === "number" ? model.contextWindow : DEFAULT_CONTEXT_WINDOW,
      maxOutputTokens: typeof model.maxTokens === "number" ? model.maxTokens : DEFAULT_MAX_TOKENS,
      supportsToolUse: true,
    }) });
  }
}
