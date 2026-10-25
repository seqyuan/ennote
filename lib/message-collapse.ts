/**
 * Decide whether a user message needs the "show full text" affordance.
 *
 * The threshold is a cheap approximation: the real height depends on the
 * rendered Markdown (tables, code fences), but character and line counts cover
 * the two common cases (pasted logs and long prose) without measuring the DOM
 * on every streaming delta.
 */
export const MESSAGE_COLLAPSE_CHARS = 1200;
export const MESSAGE_COLLAPSE_LINES = 14;

export function shouldCollapseMessage(
  text: string,
  { charLimit = MESSAGE_COLLAPSE_CHARS, lineLimit = MESSAGE_COLLAPSE_LINES } = {},
): boolean {
  if (text.length > charLimit) return true;
  return text.split(/\r?\n/).length > lineLimit;
}
