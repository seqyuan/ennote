"use client";

import { Users } from "lucide-react";
import { useState, useCallback, useMemo } from "react";
import { useT } from "@/components/LocaleProvider";
import type { ApprovalDecision, ApprovalItem, ToolApprovalRequest } from "@/lib/approval";

const RISK_KEYS: Record<string, string> = {
  read_only: "conversation.tool.risk.readOnly",
  local_write: "conversation.tool.risk.localWrite",
  shell: "conversation.tool.risk.shell",
  external: "conversation.tool.risk.external",
  delegation: "conversation.tool.risk.delegation",
  sensitive: "conversation.tool.risk.sensitive",
};

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
  const t = useT();
  if (!item.standingScope) return null;
  return <label className="standing-remember">
    <input type="checkbox" checked={checked}
      onChange={() => onChange(item.callIndex)} />
    {t("conversation.approval.remember").replace("{scope}", item.standingScope.display)}
  </label>;
}

function DelegationPreview({ item }: { item: ApprovalItem }) {
  const t = useT();
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
            {t("conversation.approval.skills")}: {(delegation.skills ?? []).join(", ")}
          </small>}
          {Boolean(delegation.depends?.length) && <small className="approval-delegation-meta">
            {t("conversation.approval.after")}: {(delegation.depends ?? []).join(", ")}
          </small>}
        </span>
        <span className="approval-delegation-budget">
          {delegation.budget.maxModelCalls} {t("conversation.approval.modelCalls")} · {delegation.budget.maxToolCalls} {t("conversation.approval.toolCalls")}
        </span>
      </div>;
    })}
  </div>;
}

export function ApprovalPanel({ approval, resolving, decide }: ApprovalPanelProps) {
  const t = useT();
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

  const batchLabel = (approval.items.length === 1 ? t("conversation.approval.batchOne") : t("conversation.approval.batchMany"))
    .replace("{iteration}", String(approval.iteration))
    .replace("{count}", String(approval.items.length));
  return <section className="approval-panel" aria-label={t("conversation.approval.panelAria")}>
    <div className="approval-heading">
      <div>
        <strong>{delegationAdmission ? t("conversation.approval.delegationRequired") : t("conversation.approval.required")}</strong>
        {approval.attribution?.speakerKind === "role" && <span className="approval-attribution">
          @{approval.attribution.handle || approval.attribution.displayName} · {approval.attribution.authority} · {approval.attribution.permissionCeiling}
        </span>}
        <span>{batchLabel}</span>
      </div>
      <span className="approval-waiting">{t("conversation.approval.waiting")}</span>
    </div>
    <div className="approval-items">
      {approval.items.map(item => <div className="approval-item" key={`${item.callIndex}-${item.toolCallId}`}>
        <div className="approval-item-title">
          <code>{item.toolName}</code>
          <span data-risk={item.riskClass}>{t(RISK_KEYS[item.riskClass] ?? "conversation.tool.risk.sensitive")}</span>
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
        onClick={handleReject}>{resolving === "rejected" ? t("conversation.approval.rejecting") : t("conversation.approval.rejectBatch")}</button>
      <button type="button" disabled={Boolean(resolving)}
        onClick={handleApprove}>{resolving === "approved" ? t("conversation.approval.approving") : t("conversation.approval.approveBatch")}</button>
    </div>
  </section>;
}
