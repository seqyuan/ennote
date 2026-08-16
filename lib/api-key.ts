/**
 * Failure copy key for a typed API key, or `undefined` when the field is
 * acceptable as-is. An empty field is not a failure at all — it means "keep the
 * stored key" on an editor card, or "authenticate some other way" on a create
 * card. A whitespace-only field fails rather than being silently dropped.
 * @param draft - raw key text from the field.
 * @returns the failure key, or `undefined` when acceptable.
 */
export function apiKeyFailure(draft: string): "keyBlank" | "keyIllegalCharacters" | undefined {
  const trimmed = draft.trim();
  if (trimmed.length === 0) return draft.length === 0 ? undefined : "keyBlank";
  // Printable ASCII only: exactly what an HTTP header value can carry.
  if (/[^\x21-\x7E]/.test(trimmed)) return "keyIllegalCharacters";
  // Refuse a pasted `NAME=value` environment line or a value wrapped in
  // matching quotes — the two paste shapes a user reaches for instead of the
  // bare key.
  if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(trimmed)) return "keyIllegalCharacters";
  if ((trimmed.startsWith("\"") && trimmed.endsWith("\""))
    || (trimmed.startsWith("'") && trimmed.endsWith("'"))) return "keyIllegalCharacters";
  return undefined;
}
