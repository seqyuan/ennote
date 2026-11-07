import type { components } from "@/lib/worker-api.gen";

/** The declared shape of a tool_output_delta payload. */
export type ToolOutputDelta = components["schemas"]["ToolOutputDeltaPayload"];

/**
 * One identity fallback for live tool events.
 *
 * `tool_call_started/completed` and `tool_output_delta` must agree on the same
 * key or a single tool call splits into two activities: one that receives the
 * result and one that keeps streaming text. Canonical messages use
 * `part.toolCall.id`; live events fall back through the same fields here.
 */
export function toolCallIdOf(payload: Record<string, unknown>): string {
  const value = payload.toolCallId ?? payload.id ?? payload.recordId;
  if (value !== undefined && value !== null) return String(value);
  return `event-${payload.callIndex ?? "unknown"}`;
}

/**
 * Narrows a live payload to the tool_output_delta shape the contract declares.
 *
 * The frame's payload is typed as open in the API, so this is the only place the
 * declared shape is enforced: a producer that renames a field, drops the identity or
 * sends an unknown stream stops feeding the preview instead of quietly drawing
 * output on the wrong card.
 */
export function toolOutputDeltaOf(payload: Record<string, unknown>): ToolOutputDelta | null {
  const toolCallId = payload.toolCallId;
  const stream = payload.stream;
  const text = payload.text;
  // An empty id is rejected here but kept by toolCallIdOf above. The asymmetry is
  // deliberate: an empty id is a legitimate "this call has no id yet" card key, and
  // a payload that carries one is not a delta we can place.
  if (typeof toolCallId !== "string" || toolCallId === "") return null;
  // Exhaustive against the contract on purpose: adding a stream there has to be a
  // compile error here, or the client would silently drop frames for it.
  const streams: Record<ToolOutputDelta["stream"], true> = { stdout: true, stderr: true, progress: true };
  const knownStream = typeof stream === "string" && streams[stream as ToolOutputDelta["stream"]]
    ? stream as ToolOutputDelta["stream"]
    : null;
  if (!knownStream) return null;
  if (typeof text !== "string") return null;
  const toolName = payload.toolName;
  if (toolName !== undefined && typeof toolName !== "string") return null;
  return { toolCallId, stream: knownStream, text, ...(toolName === undefined ? {} : { toolName }) };
}
