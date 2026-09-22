import type { ConversationNode } from "./chat-messages";

/**
 * The Run an inspector should open when no Run is active: the newest Run the
 * timeline knows about. Inspection has to stay available after a Run settles,
 * otherwise the record can only be read while it is still running.
 *
 * Scans backwards and returns the first Turn that carries a Run id, so a
 * checkpoint node or an older Turn never wins over the newest Run.
 */
export function latestRunId(nodes: ConversationNode[]): string | null {
  for (let index = nodes.length - 1; index >= 0; index -= 1) {
    const node = nodes[index];
    if (node.kind === "turn" && node.runId) return node.runId;
  }
  return null;
}

/** Digest prefix for display; the full value stays in the title attribute. */
export function shortDigest(digest: string, length = 12): string {
  if (!digest) return "";
  return digest.length <= length ? digest : `${digest.slice(0, length)}…`;
}

/**
 * Section kinds in composition order as a stable list, so a reader can scan a
 * Run's composition without re-deriving precedence from the data.
 */
export const PROMPT_SECTION_KINDS = ["base", "role", "context", "skills_catalog", "skill_preload"] as const;

/** Human byte size for a section or prompt: exact for small, coarse above a KiB. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}
