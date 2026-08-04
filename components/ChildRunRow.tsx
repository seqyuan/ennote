"use client";

import { Bot, CircleAlert, CircleCheck, Clock3, PauseCircle, XCircle } from "lucide-react";
import type { components } from "@/lib/worker-api.gen";
import { boundedToolOutput } from "@/lib/tool-presentation";

type ChildActivity = components["schemas"]["DelegationChildActivity"];

type ChildState = "queued" | "running" | "approval" | "completed" | "failed" | "interrupted" | "cancelled";

export function ChildRunRow({ child }: { child: ChildActivity }) {
  const state = childState(child);
  const result = child.result?.summary || child.errorMessage || "";
  return <div className="child-run-row" data-child-run-id={child.childRunId} data-state={state} role="listitem">
    <span className="child-run-icon"><Bot size={14} aria-hidden="true" /></span>
    <span className="child-run-identity">
      <strong>{child.name}</strong>
      <span>@{child.roleHandle}</span>
    </span>
    <span className="child-run-status">{stateIcon(state)}{stateLabel(state)}</span>
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
