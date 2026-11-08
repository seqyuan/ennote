/**
 * Composer trigger detection.
 *
 * The picker opens because of what is at the caret, not because of what the whole
 * draft starts with: "look at this @sec" has to work, and `foo@bar.com` or
 * `src/app` must not. Detection is therefore caret-relative and boundary-aware,
 * and it returns the query the user has typed after the trigger so the picker can
 * filter without owning an input of its own.
 */

export type TriggerKind = "all" | "hero" | "graph" | "skill" | "template";

export interface ComposerTrigger {
  kind: TriggerKind;
  /** Text between the trigger and the caret; never contains whitespace. */
  query: string;
  /** Index of the trigger character, so the picker can replace the token. */
  start: number;
}

/** Characters that may sit between `/` and the trigger without making it a path. */
const NOT_A_PATH_BEFORE_SLASH = new Set([":", "/", "\\", "."]);

function isWordCharacter(char: string): boolean {
  return /[A-Za-z0-9_]/.test(char);
}

function isBoundary(previous: string | undefined): boolean {
  // Start of input, whitespace, or punctuation all count as a boundary. What must
  // not count is a letter/digit: that is where `foo@bar` and `src/app` live.
  return previous === undefined || !isWordCharacter(previous);
}

/** Split `@role:sec` / `@graph:go@2` / `/skill:cell-qc` into kind + query. */
function parseToken(symbol: "@" | "/", token: string): ComposerTrigger {
  const query = token.toLowerCase();
  const [head, ...rest] = query.split(":");
  const tail = rest.join(":");
  if (symbol === "@") {
    // `@hero:` is the canonical spelling; `@role:` stays an input alias so old
    // muscle memory and scripts keep working.
    if (head === "hero" || head === "role") return { kind: "hero", query: tail, start: 0 };
    if (head === "graph" || head === "flow") return { kind: "graph", query: tail, start: 0 };
    return { kind: "all", query, start: 0 };
  }
  if (head === "skill") return { kind: "skill", query: tail, start: 0 };
  if (head === "template") return { kind: "template", query: tail, start: 0 };
  return { kind: "skill", query, start: 0 };
}

/**
 * Find the trigger the caret sits inside, if any.
 *
 * @param text   the whole draft
 * @param caret  UTF-16 offset into `text`
 */
export function detectComposerTrigger(text: string, caret: number): ComposerTrigger | null {
  if (!Number.isFinite(caret) || caret < 0 || caret > text.length) return null;
  const before = text.slice(0, caret);
  let start = -1;
  for (let index = before.length - 1; index >= 0; index -= 1) {
    const char = before[index];
    if (char === "@" || char === "/") {
      start = index;
      break;
    }
    // A space ends the token: the trigger must be the nearest non-space run.
    if (/\s/.test(char)) return null;
  }
  if (start < 0) return null;

  const symbol = before[start] as "@" | "/";
  const previous = start > 0 ? before[start - 1] : undefined;
  if (!isBoundary(previous)) return null;
  if (symbol === "/" && previous !== undefined && NOT_A_PATH_BEFORE_SLASH.has(previous)) return null;

  const trigger = parseToken(symbol, before.slice(start + 1));
  return { ...trigger, start };
}
