import type { ModelProfile, ProviderProfile } from "@/components/settings/types";
import type { ThinkingEffort } from "@/lib/permission-mode";

/**
 * Composer model-seat mapping against dsh's Host catalog:
 *
 * dsh advertises `model.reasoning?: { efforts, defaultEffort? }`. Absence
 * hides the Effort row. Ennote's equivalent is `supportsThinking` — the
 * Worker catalog's `thinking` / `thinkingDialect` pair, already collapsed
 * onto ModelProfile by the models store. A non-thinking model (dialect
 * `none`, efforts `["default"]`) does not expose an Effort row.
 *
 * Effort ids stay the Worker's closed enum (`default|low|medium|high`).
 * Display names are the capitalized id ("High"), matching dsh DeepSeek's
 * Host-supplied `effort.name` rather than a client-owned subtitle.
 *
 * dsh `defaultEffort` maps to Ennote `"default"`: choosing a model without
 * an explicit effort (dsh omits `reasoningEffort` on the select RPC) lands
 * on this value.
 */
export function modelHasReasoning(model: Pick<ModelProfile, "supportsThinking"> | null | undefined): boolean {
  return model?.supportsThinking === true;
}

export function reasoningEfforts(model: Pick<ModelProfile, "supportsThinking" | "supportedThinkingEfforts"> | null | undefined): readonly ThinkingEffort[] {
  if (!modelHasReasoning(model)) return [];
  const declared = model?.supportedThinkingEfforts;
  return declared && declared.length > 0 ? declared : ["default"];
}

/** dsh Host `effort.name`: capitalize the Worker id ("default" → "Default"). */
export function effortDisplayName(level: string): string {
  return level.charAt(0).toUpperCase() + level.slice(1);
}

export type ModelProviderGroup = {
  id: string;
  name: string;
  models: ModelProfile[];
};

/** Provider-grouped catalog, using the provider display name like dsh `group.name`. */
export function groupModelsByProvider(
  models: readonly ModelProfile[],
  providers: readonly Pick<ProviderProfile, "id" | "name">[],
): ModelProviderGroup[] {
  const nameById = new Map(providers.map((provider) => [provider.id, provider.name]));
  const order: string[] = [];
  const byProvider = new Map<string, ModelProfile[]>();
  for (const model of models) {
    const list = byProvider.get(model.providerId);
    if (list) {
      list.push(model);
      continue;
    }
    order.push(model.providerId);
    byProvider.set(model.providerId, [model]);
  }
  return order.map((id) => ({
    id,
    name: nameById.get(id) || id,
    models: byProvider.get(id) ?? [],
  }));
}
