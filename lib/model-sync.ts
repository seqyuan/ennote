import type { ModelDraft } from "./model-draft";

/** The minimal shape of an existing model profile the sync planner needs. */
export interface BeforeModel {
  /** Full model profile id, e.g. "openai-main/gpt-4o". */
  id: string;
  modelName: string;
  displayName: string;
  contextWindow: number;
  maxOutputTokens: number;
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

/** A model-profile update payload (null clears the field to its default). */
export interface ModelUpdateInput {
  displayName: string | null;
  contextWindow: number | null;
  maxOutputTokens: number | null;
}

/** What a provider-edit Apply must do to reconcile the drafted list. */
export interface ModelSyncPlan {
  /** Create these models (drafted ids with no existing profile). */
  toCreate: ModelCreateInput[];
  /** Delete these existing model profile ids (removed from the draft). */
  toDelete: string[];
  /** Update these existing models (fields the draft changed). */
  toUpdate: Array<{ id: string; input: ModelUpdateInput }>;
}

function textField(draft: ModelDraft, key: string): string | undefined {
  const value = draft[key];
  return typeof value === "string" && value.trim().length > 0 ? value.trim() : undefined;
}

function numberField(draft: ModelDraft, key: string): number | undefined {
  const value = draft[key];
  return typeof value === "number" ? value : undefined;
}

/**
 * Whether the draft's edited fields differ from the existing resolved model.
 * A cleared field (absent from the draft) reads as a change when the existing
 * profile carries a value, so an explicit clear lands as a null in the update.
 * @param draft - the drafted row.
 * @param before - the provider's current resolved model.
 */
export function draftChanged(draft: ModelDraft, before: BeforeModel): boolean {
  const name = textField(draft, "name");
  const beforeName = before.displayName && before.displayName !== before.modelName
    ? before.displayName
    : undefined;
  if (name !== beforeName) return true;
  const contextWindow = numberField(draft, "contextWindow");
  if (contextWindow !== before.contextWindow) return true;
  const maxTokens = numberField(draft, "maxTokens");
  if (maxTokens !== before.maxOutputTokens) return true;
  return false;
}

/**
 * Plan the reconciliation between the existing models of a provider and the
 * drafted list the editor holds. Models present in both are updated in place
 * (the API now has an update endpoint); new ids are created and dropped ids
 * are deleted.
 * @param providerId - provider key.
 * @param before - the provider's current model profiles.
 * @param after - the drafted rows (from {@link ModelDraft}).
 * @param fallbackContextWindow - capacity used when a created row omits it.
 * @param fallbackMaxTokens - same, for the output cap.
 */
export function planModelSync(
  providerId: string,
  before: readonly BeforeModel[],
  after: readonly ModelDraft[],
  fallbackContextWindow = 131072,
  fallbackMaxTokens = 16384,
): ModelSyncPlan {
  const plan: ModelSyncPlan = { toCreate: [], toDelete: [], toUpdate: [] };
  const beforeByKey = new Map(before.map(model => [model.modelName, model]));
  const afterIds = new Set<string>();
  for (const draft of after) {
    const id = textField(draft, "id");
    if (id === undefined) continue;
    afterIds.add(id);
    const existing = beforeByKey.get(id);
    if (existing === undefined) {
      plan.toCreate.push({
        providerId,
        modelName: id,
        ...(textField(draft, "name") === undefined ? {} : { displayName: textField(draft, "name") }),
        contextWindow: numberField(draft, "contextWindow") ?? fallbackContextWindow,
        maxOutputTokens: numberField(draft, "maxTokens") ?? fallbackMaxTokens,
        supportsToolUse: true,
      });
      continue;
    }
    if (draftChanged(draft, existing)) {
      plan.toUpdate.push({
        id: existing.id,
        input: {
          displayName: textField(draft, "name") ?? null,
          contextWindow: numberField(draft, "contextWindow") ?? null,
          maxOutputTokens: numberField(draft, "maxTokens") ?? null,
        },
      });
    }
  }
  for (const model of before) {
    if (!afterIds.has(model.modelName)) plan.toDelete.push(model.id);
  }
  return plan;
}
