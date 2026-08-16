/** One configured model row, kept structurally open so hidden or future fields
 *  survive an edit. The card edits id/name/contextWindow/maxTokens; everything
 *  else passes through untouched. */
export type ModelDraft = Record<string, unknown>;

/** A localized validation failure for one user-owned model array. */
export interface ModelsValidationFailure {
  /** Zero-based model position. */
  index: number;
  /** Copy key owned by the Models settings section. */
  key:
    | "modelIdRequired"
    | "modelIdDuplicate"
    | "modelNameInvalid"
    | "modelContextInvalid"
    | "modelMaxTokensInvalid";
}

/** Convert a schema-validated catalog value into records without dropping hidden fields. */
export function modelDrafts(value: unknown): ModelDraft[] {
  if (!Array.isArray(value)) return [];
  return value.map(entry =>
    typeof entry === "object" && entry !== null && !Array.isArray(entry)
      ? entry as ModelDraft
      : {},
  );
}

/**
 * Validate the adapter constraints the serialized schema cannot express.
 * @param value - user-owned `models` value, or `undefined` while inherited.
 * @returns the first invalid row, or `undefined` when the adapter will accept it.
 */
export function validateModels(value: unknown): ModelsValidationFailure | undefined {
  if (value === undefined) return undefined;
  const models = modelDrafts(value);
  const seen = new Set<string>();
  for (const [index, model] of models.entries()) {
    // Compared trimmed: surrounding whitespace is a paste artifact.
    const id = model.id;
    const trimmed = typeof id === "string" ? id.trim() : undefined;
    if (trimmed === undefined || trimmed.length === 0) return { index, key: "modelIdRequired" };
    if (seen.has(trimmed)) return { index, key: "modelIdDuplicate" };
    seen.add(trimmed);
    const name = model.name;
    if (name !== undefined && (typeof name !== "string" || name.length === 0)) {
      return { index, key: "modelNameInvalid" };
    }
    const contextWindow = model.contextWindow;
    if (contextWindow !== undefined
      && (typeof contextWindow !== "number" || !Number.isInteger(contextWindow) || contextWindow <= 0)) {
      return { index, key: "modelContextInvalid" };
    }
    const maxTokens = model.maxTokens;
    if (maxTokens !== undefined
      && (typeof maxTokens !== "number" || !Number.isInteger(maxTokens) || maxTokens <= 0)) {
      return { index, key: "modelMaxTokensInvalid" };
    }
  }
  return undefined;
}
