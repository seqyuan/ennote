// Shared helper for mocking the session change feed (GET /v1/sessions/{id}/events).
// The feed is a server-sent event stream; tests replace it with a single-shot
// "subscribed" frame followed by EOF. The client SessionStore reconnects with
// ~1s backoff and re-fetches, so the feed becomes an effective 1s poll of the
// snapshot passed here — enough for assertions on eventual convergence.

/** Build a single subscribed SSE frame body carrying the given snapshot. */
export function subscribedFrame(snapshot: unknown, instanceId = "e2e-worker"): string {
  return `data: ${JSON.stringify({ type: "subscribed", instanceId, lastSeq: 0, snapshot })}\n\n`;
}

/** An empty snapshot (no active run, no approval, no queued inputs). */
export function idleSnapshot() {
  return { activeRun: null, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false };
}
