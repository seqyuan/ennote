"use client";

import { Users } from "lucide-react";
import { useState, useCallback, useMemo } from "react";
import { approvalRiskLabel, type ApprovalDecision, type ApprovalItem, type ToolApprovalRequest } from "@/lib/approval";

interface ApprovalPanelProps {
  approval: ToolApprovalRequest;
  resolving: ApprovalDecision | null;
  decide: (decision: ApprovalDecision, standingGrantCallIndexes?: number[]) => void;
}

function StandingCheckbox({ item, checked, onChange }: {
  item: ApprovalItem;
  checked: boolean;
  onChange: (callIndex: number) => void;
}) {
  if (!item.standingScope) return null;
  return <label className="standing-remember">
    <input type="checkbox" checked={checked}
      onChange={() => onChange(item.callIndex)} />
    Remember for {item.standingScope.display} in this session
  </label>;
}

function DelegationPreview({ item }: { item: ApprovalItem }) {
  if (!item.delegations?.length) return <pre>{item.argumentsPreview}</pre>;
  return <div className="approval-delegations">
    {item.delegations.map((delegation, index) => {
      const role = delegation.role || delegation.roleHandle;
      const goal = delegation.goalPreview || delegation.assignmentPreview;
      return <div className="approval-delegation" key={`${role}-${index}`}>
        <span className="approval-delegation-icon"><Users size={14} aria-hidden="true" /></span>
        <span className="approval-delegation-copy">
          <strong>{delegation.name}</strong>
          <span>@{role}</span>
          <small>{goal}</small>
          {Boolean(delegation.skills?.length) && <small className="approval-delegation-meta">
            skills: {(delegation.skills ?? []).join(", ")}
          </small>}
          {Boolean(delegation.depends?.length) && <small className="approval-delegation-meta">
            after: {(delegation.depends ?? []).join(", ")}
          </small>}
        </span>
        <span className="approval-delegation-budget">
          {delegation.budget.maxModelCalls} model · {delegation.budget.maxToolCalls} tool
        </span>
      </div>;
    })}
  </div>;
}

export function ApprovalPanel({ approval, resolving, decide }: ApprovalPanelProps) {
  const [selection, setSelection] = useState<{ approvalID: string; indexes: number[] }>(() => ({
    approvalID: approval.id,
    indexes: [],
  }));
  const selectedIndexes = useMemo(
    () => selection.approvalID === approval.id ? selection.indexes : [],
    [approval.id, selection],
  );
  const selectedSet = useMemo(() => new Set(selectedIndexes), [selectedIndexes]);
  const delegationAdmission = approval.items.some(item =>
    item.toolName === "delegate_tasks" || item.toolName === "delegate_roles");

  const toggleIndex = useCallback((callIndex: number) => {
    setSelection((current) => {
      const next = new Set(current.approvalID === approval.id ? current.indexes : []);
      if (next.has(callIndex)) {
        next.delete(callIndex);
      } else {
        next.add(callIndex);
      }
      return { approvalID: approval.id, indexes: Array.from(next).sort((a, b) => a - b) };
    });
  }, [approval.id]);

  const handleApprove = useCallback(() => {
    decide("approved", selectedIndexes);
  }, [decide, selectedIndexes]);

  const handleReject = useCallback(() => {
    decide("rejected", []);
  }, [decide]);

  return <section className="approval-panel" aria-label="Tool approval required">
    <div className="approval-heading">
      <div>
        <strong>{delegationAdmission ? "Delegation approval required" : "Approval required"}</strong>
        {approval.attribution?.speakerKind === "role" && <span className="approval-attribution">
          @{approval.attribution.handle || approval.attribution.displayName} · {approval.attribution.authority} · {approval.attribution.permissionCeiling}
        </span>}
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
        {item.toolName === "delegate_tasks" || item.toolName === "delegate_roles"
          ? <DelegationPreview item={item} /> : <pre>{item.argumentsPreview}</pre>}
        <StandingCheckbox item={item}
          checked={selectedSet.has(item.callIndex)}
          onChange={toggleIndex} />
      </div>)}
    </div>
    <div className="approval-actions">
      <button type="button" className="secondary-btn" disabled={Boolean(resolving)}
        onClick={handleReject}>{resolving === "rejected" ? "Rejecting…" : "Reject batch"}</button>
      <button type="button" disabled={Boolean(resolving)}
        onClick={handleApprove}>{resolving === "approved" ? "Approving…" : "Approve batch"}</button>
    </div>
  </section>;
}
