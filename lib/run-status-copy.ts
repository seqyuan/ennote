// Status strings emitted by the run event stream are stable internal codes
// (they are compared in ChatWindow/useAgentSession), so translate them at the
// render boundary instead of at the source.
const STATUS_KEYS: Record<string, string> = {
  "Waiting for your answer": "run.status.waitingAnswer",
  "Waiting for approval": "run.status.waitingApproval",
  "Answer submitted. Resuming…": "run.status.answerSubmitted",
};

export function runStatusMessageKey(status: string): string | undefined {
  return STATUS_KEYS[status];
}
