"use client";

import { Bot, CircleAlert, CircleCheck, Clock3, MessageSquareMore, PauseCircle, RotateCcw, XCircle } from "lucide-react";
import type { components } from "@/lib/worker-api.gen";
import { boundedToolOutput } from "@/lib/tool-presentation";

type ChildActivity = components["schemas"]["DelegationChildActivity"];

type ChildState = "queued" | "running" | "approval" | "completed" | "failed" | "blocked" | "interrupted" | "cancelled";

// ChildRunRow renders one delegated child. Retry-eligible rows expose an icon
// Retry command; needs_input rows expose Reply; completed/blocked rows expose
// a private Follow up command. reused marks a sibling reused without rerun.
// activity is the live child_progress label (Running bash / Thinking / …)
// shown while the child is running.
export function ChildRunRow({ child, reused = false, activity, onRetry, onContinue }: {
  child: ChildActivity;
  reused?: boolean;
  activity?: string;
  onRetry?: (itemId: string) => void;
  onContinue?: (itemId: string, kind: "input" | "follow_up") => void;
}) {
  const state = childState(child);
  const result = child.result?.summary || child.errorMessage || "";
  const retryable = onRetry !== undefined &&
    (state === "failed" || state === "interrupted" || state === "cancelled");
  const continuationKind = onContinue !== undefined ? continuationState(child) : null;
  const liveActivity = (state === "running" || state === "queued") && activity ? activity : undefined;
  return <div className="child-run-row" data-child-run-id={child.childRunId} data-state={state} role="listitem">
    <span className="child-run-icon"><Bot size={14} aria-hidden="true" /></span>
    <span className="child-run-identity">
      <strong>{child.name}</strong>
      <span>@{child.roleHandle}</span>
      {reused && <span className="child-run-reused">Reused</span>}
    </span>
    <span className="child-run-actions">
      {retryable && <button type="button" className="child-run-retry"
        aria-label={`Retry ${child.name}`} title="Retry this delegated task"
        onClick={() => onRetry(child.itemId)}>
        <RotateCcw size={13} aria-hidden="true" />
      </button>}
      {continuationKind && onContinue && <button type="button" className="child-run-retry"
        aria-label={`${continuationKind === "input" ? "Reply" : "Follow up"} ${child.name}`}
        title={continuationKind === "input" ? "Reply to this delegated task" : "Follow up privately"}
        onClick={() => onContinue(child.itemId, continuationKind)}>
        <MessageSquareMore size={13} aria-hidden="true" />
      </button>}
      <span className="child-run-status">{stateIcon(state)}{stateLabel(state)}
        {liveActivity && <span className="child-run-activity" title={liveActivity}>{liveActivity}</span>}
        {errorCodeLabel(child) && (state === "failed" || state === "blocked") &&
          <span className="child-run-error-code">{errorCodeLabel(child)}</span>}
      </span>
    </span>
    {result && <details className="child-run-result">
      <summary>{child.result ? "Result" : "Failure"}</summary>
      <div>{boundedToolOutput(result, 1200)}</div>
    </details>}
  </div>;
}

function childState(child: ChildActivity): ChildState {
  if (child.itemStatus === "blocked") return "blocked";
  switch (child.runStatus) {
    case "queued": return "queued";
    case "running": return "running";
    case "waiting_for_approval": return "approval";
    case "succeeded": return "completed";
    case "failed": return "failed";
    case "interrupted": return "interrupted";
    case "cancelled": return "cancelled";
  }
  if (child.itemStatus === "succeeded") return "completed";
  if (child.itemStatus === "failed" || child.itemStatus === "not_authorized") return "failed";
  if (child.itemStatus === "cancelled") return "cancelled";
  return "queued";
}

// errorCodeLabel maps known delegation terminal error codes to a stable,
// scannable label. Unknown codes fall back to the raw code.
function errorCodeLabel(child: ChildActivity): string | null {
  if (child.itemStatus === "blocked") return "Blocked";
  switch (child.errorCode) {
    case "delegation_budget_exceeded": return "Budget exceeded";
    case "delegation_not_authorized": return "Not authorized";
    case "delegation_dag_invalid": return "Blocked";
    case "tool_batch_failed": return "Tool failure";
    case "process_not_allowed": return "Process not allowed";
    case "permission_mode_sensitive": return "Sensitive action";
    default: return child.errorCode ? child.errorCode : null;
  }
}

function continuationState(child: ChildActivity): "input" | "follow_up" | null {
  const result = child.result;
  if (result?.status === "needs_input") return "input";
  if (result?.status === "blocked") return "follow_up";
  if (child.itemStatus === "succeeded") return "follow_up";
  return null;
}

function stateIcon(state: ChildState) {
  const props = { size: 13, "aria-hidden": true } as const;
  if (state === "completed") return <CircleCheck {...props} />;
  if (state === "failed") return <CircleAlert {...props} />;
  if (state === "blocked") return <PauseCircle {...props} />;
  if (state === "interrupted") return <PauseCircle {...props} />;
  if (state === "cancelled") return <XCircle {...props} />;
  return <Clock3 {...props} />;
}

function stateLabel(state: ChildState): string {
  if (state === "queued") return "Queued";
  if (state === "running") return "Running";
  if (state === "approval") return "Approval";
  if (state === "completed") return "Completed";
  if (state === "blocked") return "Blocked";
  if (state === "interrupted") return "Interrupted";
  if (state === "cancelled") return "Cancelled";
  return "Failed";
}
