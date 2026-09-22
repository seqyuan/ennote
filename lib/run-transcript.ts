import type { components } from "./worker-api.gen";

export type RunMessage = components["schemas"]["RunMessage"];
type MessageContentBlock = components["schemas"]["MessageContentBlock"];

/**
 * One displayable unit of a Run's private transcript.
 *
 * A Run's transcript holds model-visible content, so unknown block kinds are
 * kept as an explicit entry rather than dropped: a reader comparing this to the
 * prompt must be able to see that something was there.
 */
export type TranscriptEntry =
  | { kind: "text"; text: string }
  | { kind: "thinking"; text: string }
  | { kind: "tool_call"; callId: string; name: string; arguments?: string }
  | { kind: "tool_result"; callId: string; name: string; text: string; isError: boolean }
  | { kind: "image"; width: number; height: number }
  | { kind: "unknown"; type: string };

function stringField(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

/** Pretty arguments only when they are an object; a malformed blob stays as-is. */
function renderArguments(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== "object") return String(value);
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return undefined;
  }
}

/**
 * Project a transcript message's content blocks into display entries, in the
 * order the model saw them. Never throws: the log is durable and may carry
 * shapes this client version does not know.
 */
export function transcriptEntries(blocks: MessageContentBlock[] | undefined): TranscriptEntry[] {
  const entries: TranscriptEntry[] = [];
  for (const raw of blocks ?? []) {
    const block = (raw ?? {}) as Record<string, unknown>;
    switch (block.type) {
      case "text":
        entries.push({ kind: "text", text: stringField(block.text) });
        break;
      case "thinking":
        entries.push({ kind: "thinking", text: stringField(block.text) });
        break;
      case "tool_call": {
        const call = (block.toolCall ?? {}) as Record<string, unknown>;
        const argumentsText = renderArguments(call.arguments);
        entries.push({
          kind: "tool_call",
          callId: stringField(call.id),
          name: stringField(call.name, "tool") || "tool",
          ...(argumentsText === undefined ? {} : { arguments: argumentsText }),
        });
        break;
      }
      case "tool_result": {
        const result = (block.toolResult ?? {}) as Record<string, unknown>;
        entries.push({
          kind: "tool_result",
          callId: stringField(result.toolCallId),
          name: stringField(result.toolName, "tool") || "tool",
          text: stringField(result.content),
          isError: result.isError === true,
        });
        break;
      }
      case "image": {
        const image = (block.image ?? {}) as Record<string, unknown>;
        entries.push({
          kind: "image",
          width: typeof image.width === "number" ? image.width : 0,
          height: typeof image.height === "number" ? image.height : 0,
        });
        break;
      }
      case "image_description": {
        const description = (block.imageDescription ?? {}) as Record<string, unknown>;
        entries.push({ kind: "text", text: stringField(description.text) });
        break;
      }
      default:
        entries.push({ kind: "unknown", type: stringField(block.type, "unknown") || "unknown" });
    }
  }
  return entries;
}

/**
 * The tools a transcript message called, in order and de-duplicated. Used as the
 * collapsed row's summary so a reader can scan a Run without expanding it.
 */
export function transcriptSummary(blocks: MessageContentBlock[] | undefined): string {
  const seen = new Set<string>();
  const names: string[] = [];
  for (const entry of transcriptEntries(blocks)) {
    if (entry.kind !== "tool_call" || seen.has(entry.name)) continue;
    seen.add(entry.name);
    names.push(entry.name);
  }
  return names.join(", ");
}
