import type { ModelDraft } from "./model-draft";

/** The minimal shape of an existing model profile the sync planner needs. */
export interface BeforeModel {
  /** Full model profile id, e.g. "openai-main/gpt-4o". */
  id: string;
  modelName: string;
  displayName: string;
  contextWindow: number;
  maxOutputTokens: number;
  isDefault?: boolean;
}

/** A model-profile create payload. */
export interface ModelCreateInput {
  providerId: string;
  modelName: string;
  displayName?: string;
  contextWindow: number;
  maxOutputTokens: number;
  supportsToolUse: boolean;
}

/** What a provider-edit Apply must do to reconcile the drafted list. */
export interface ModelSyncPlan {
  /** Create these models (drafted ids with no existing profile). */
  toCreate: ModelCreateInput[];
  /** Delete these existing model profile ids (removed from the draft). */
  toDelete: string[];
  /** Recreate these existing profiles that changed (the API has no update). */
  toRecreate: Array<{ deleteId: string; input: ModelCreateInput; wasDefault: boolean }>;
}

/**
 * Plan the reconciliation between the existing models of a provider and the
 * drafted list the editor holds. Because the model-profiles API only creates
 * and deletes, a changed existing model is recreated (delete + create), and an
 * existing default is re-promoted after recreation.
 * @param providerId - provider key.
 * @param before - the provider's current model profiles.
 * @param after - the drafted rows (from {@link ModelDraft}).
 * @param fallbackContextWindow - capacity used when a drafted row omits it and
 * no existing profile supplies one.
 * @param fallbackMaxTokens - same, for the output cap.
 */
export function planModelSync(
  providerId: string,
  before: readonly BeforeModel[],
  after: readonly ModelDraft[],
  fallbackContextWindow = 131072,
  fallbackMaxTokens = 16384,
): ModelSyncPlan {
  const plan: ModelSyncPlan = { toCreate: [], toDelete: [], toRecreate: [] };
  const beforeByKey = new Map(before.map(model => [model.modelName, model]));
  const afterIds = new Set<string>();
  for (const draft of after) {
    const id = typeof draft.id === "string" ? draft.id.trim() : "";
    if (id.length === 0) continue;
    afterIds.add(id);
    const existing = beforeByKey.get(id);
    const contextWindow = typeof draft.contextWindow === "number"
      ? draft.contextWindow
      : existing?.contextWindow ?? fallbackContextWindow;
    const maxTokens = typeof draft.maxTokens === "number"
      ? draft.maxTokens
      : existing?.maxOutputTokens ?? fallbackMaxTokens;
    const displayName = typeof draft.name === "string" && draft.name.trim().length > 0
      ? draft.name.trim()
      : undefined;
    const input: ModelCreateInput = {
      providerId,
      modelName: id,
      ...(displayName === undefined ? {} : { displayName }),
      contextWindow,
      maxOutputTokens: maxTokens,
      supportsToolUse: true,
    };
    if (existing === undefined) {
      plan.toCreate.push(input);
      continue;
    }
    const changed = displayName !== undefined && displayName !== existing.displayName
      || contextWindow !== existing.contextWindow
      || maxTokens !== existing.maxOutputTokens;
    if (changed) {
      plan.toRecreate.push({ deleteId: existing.id, input, wasDefault: existing.isDefault === true });
    }
  }
  for (const model of before) {
    if (!afterIds.has(model.modelName)) plan.toDelete.push(model.id);
  }
  return plan;
}
