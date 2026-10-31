// Run status values compared by the session hooks are stable codes. Dynamic
// facts (tool name, delay, model) ride in a detail field. Rendering translates
// the code; it does not match English sentences.

const SEP = "\u001f";

export const RUN_STATUS_RECONNECTING = "reconnecting";

/** Every status code the Worker can publish, mapped to its copy key. Exported
 *  so tests can pin one key per code and catch copy drift in either locale. */
export const STATUS_KEYS: Record<string, string> = {
  waiting_answer: "run.status.waitingAnswer",
  waiting_approval: "run.status.waitingApproval",
  waiting_delegation_admission: "run.status.waitingDelegation",
  waiting_children: "run.status.waitingChildren",
  running: "run.status.running",
  running_tool: "run.status.runningTool",
  reconnecting: "run.status.reconnecting",
  steering: "run.status.steering",
  cancelled: "run.status.cancelled",
  compacting: "run.status.compacting",
  compaction_queued: "run.status.compactionQueued",
  compaction_retry_wait: "run.status.compactionRetryWait",
  compaction_retry: "run.status.compactionRetry",
  checkpoint_ready: "run.status.checkpointReady",
  answer_submitted: "run.status.answerSubmitted",
  approval_granted: "run.status.approvalGranted",
  approval_rejected: "run.status.approvalRejected",
  resuming: "run.status.resuming",
  model_retry: "run.status.modelRetry",
  preparing_context: "run.status.preparingContext",
  preparing_run_context: "run.status.preparingRunContext",
  compacting_run: "run.status.compactingRun",
  run_compaction_retry: "run.status.runCompactionRetry",
  run_context_compacted: "run.status.runContextCompacted",
  run_compaction_skipped: "run.status.runCompactionSkipped",
  using_checkpoint: "run.status.usingCheckpoint",
  recovering_tool: "run.status.recoveringTool",
  output_truncated: "run.status.outputTruncated",
  using_model: "run.status.usingModel",
  describing_image: "run.status.describingImage",
  image_ready: "run.status.imageReady",
  completed: "run.status.completed",
  interrupted: "run.status.interrupted",
  sending: "run.status.sending",
  uploading_image: "run.status.uploadingImage",
};

export function encodeRunStatus(code: string, detail?: string): string {
  if (!detail) return code;
  return `${code}${SEP}${detail}`;
}

export function decodeRunStatus(status: string): { code: string; detail: string } {
  const index = status.indexOf(SEP);
  if (index < 0) return { code: status, detail: "" };
  return { code: status.slice(0, index), detail: status.slice(index + SEP.length) };
}

export function runStatusText(status: string, t: (key: string) => string): string {
  const { code, detail } = decodeRunStatus(status);
  const key = STATUS_KEYS[code];
  // Unmapped codes still carry a detail: show the detail, never the encoded
  // `code\u001Fdetail` wire form.
  if (!key) return detail || humanizeStatus(code);
  // split/join keeps `$&`, "$`" and "$'" in a detail (tool output, model name)
  // literal; replaceAll would read them as replacement patterns.
  return t(key).split("{detail}").join(detail);
}

function humanizeStatus(code: string): string {
  return code.replaceAll("_", " ");
}
