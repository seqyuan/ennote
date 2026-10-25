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
