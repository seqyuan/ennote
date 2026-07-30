"use client";

import { approvalRiskLabel, type ApprovalDecision, type ToolApprovalRequest } from "@/lib/approval";

interface ApprovalPanelProps {
  approval: ToolApprovalRequest;
  resolving: ApprovalDecision | null;
  decide: (decision: ApprovalDecision) => void;
}

export function ApprovalPanel({ approval, resolving, decide }: ApprovalPanelProps) {
  return <section className="approval-panel" aria-label="Tool approval required">
    <div className="approval-heading">
      <div>
        <strong>Approval required</strong>
        <span>Batch {approval.iteration} · {approval.items.length} {approval.items.length === 1 ? "action" : "actions"}</span>
      </div>
      <span className="approval-waiting">Waiting</span>
    </div>
    <div className="approval-items">
      {approval.items.map(item => <div className="approval-item" key={`${item.callIndex}-${item.toolCallId}`}>
        <div className="approval-item-title">
          <code>{item.toolName}</code>
          <span data-risk={item.riskClass}>{approvalRiskLabel(item.riskClass)}</span>
        </div>
        <pre>{item.argumentsPreview}</pre>
      </div>)}
    </div>
    <div className="approval-actions">
      <button type="button" className="secondary-btn" disabled={Boolean(resolving)}
        onClick={() => decide("rejected")}>{resolving === "rejected" ? "Rejecting…" : "Reject batch"}</button>
      <button type="button" disabled={Boolean(resolving)}
        onClick={() => decide("approved")}>{resolving === "approved" ? "Approving…" : "Approve batch"}</button>
    </div>
  </section>;
}
