/**
 * Format a text selection as a composer quote.
 *
 * The quote is bounded so a large table or log paste cannot flood the input.
 * The source marker is plain text (not Markdown) so it survives the composer
 * and the submitted turn without relying on renderer support.
 */

export const MAX_QUOTE_CHARS = 4000;

export function normalizeSelection(text: string): string {
  return text
    .replace(/\r\n/g, "\n")
    .replace(/[ \t]+\n/g, "\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

export function buildQuote(text: string, sourceLabel: string): string | null {
  const normalized = normalizeSelection(text);
  if (!normalized) return null;
  const clipped = normalized.length > MAX_QUOTE_CHARS
    ? `${normalized.slice(0, MAX_QUOTE_CHARS).trimEnd()}…`
    : normalized;
  const quoted = clipped.split("\n").map((line) => (line.length > 0 ? `> ${line}` : ">")).join("\n");
  return `${quoted}\n>\n> — ${sourceLabel}`;
}

export function buildExplainRequest(prompt: string, quote: string): string {
  return `${prompt}\n\n${quote}`;
}
