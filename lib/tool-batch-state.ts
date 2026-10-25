import type { ToolBatchStep } from "./chat-messages";

/**
 * Stable identity for a rendered tool batch.
 *
 * `batch.id` is derived from the first activity's row id, and the live and
 * canonical projections build different row ids for the same call. The
 * `toolCallId` is the only value both sides share, so it also keys React state
 * across the transient → canonical handoff.
 */
export function toolBatchKey(batch: Pick<ToolBatchStep, "id" | "activities">): string {
  return batch.activities[0]?.toolCallId || batch.id;
}

/**
 * A folded batch may hide process, but it must not hide results: artifacts,
 * delegation activity, and failures stay visible by default.
 */
export function shouldExpandBatch({ isLast, hasActive, hasApproval, hasOutcome }: {
  isLast: boolean;
  hasActive: boolean;
  hasApproval: boolean;
  hasOutcome: boolean;
}): boolean {
  return isLast || hasActive || hasApproval || hasOutcome;
}
