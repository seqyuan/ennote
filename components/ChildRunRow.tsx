"use client";

import { Bot, CircleAlert, CircleCheck, Clock3, PauseCircle, RotateCcw, XCircle } from "lucide-react";
import type { components } from "@/lib/worker-api.gen";
import { boundedToolOutput } from "@/lib/tool-presentation";

type ChildActivity = components["schemas"]["DelegationChildActivity"];

type ChildState = "queued" | "running" | "approval" | "completed" | "failed" | "interrupted" | "cancelled";

// ChildRunRow renders one delegated child. When onRetry is provided and the
// child is retry-eligible, an icon Retry command is exposed with a tooltip.
// reused marks a sibling reused by a later generation without re-execution.
export function ChildRunRow({ child, reused = false, onRetry }: {
  child: ChildActivity;
  reused?: boolean;
  onRetry?: (itemId: string) => void;
}) {
  const state = childState(child);
  const result = child.result?.summary || child.errorMessage || "";
  const retryable = onRetry !== undefined &&
    (state === "failed" || state === "interrupted" || state === "cancelled");
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
      <span className="child-run-status">{stateIcon(state)}{stateLabel(state)}</span>
    </span>
    {result && <details className="child-run-result">
      <summary>{child.result ? "Result" : "Failure"}</summary>
      <div>{boundedToolOutput(result, 1200)}</div>
    </details>}
  </div>;
}

function childState(child: ChildActivity): ChildState {
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

function stateIcon(state: ChildState) {
  const props = { size: 13, "aria-hidden": true } as const;
  if (state === "completed") return <CircleCheck {...props} />;
  if (state === "failed") return <CircleAlert {...props} />;
  if (state === "interrupted") return <PauseCircle {...props} />;
  if (state === "cancelled") return <XCircle {...props} />;
  return <Clock3 {...props} />;
}

function stateLabel(state: ChildState): string {
  if (state === "queued") return "Queued";
  if (state === "running") return "Running";
  if (state === "approval") return "Approval";
  if (state === "completed") return "Completed";
  if (state === "interrupted") return "Interrupted";
  if (state === "cancelled") return "Cancelled";
  return "Failed";
}
