"use client";

import { CircleAlert, MessageSquareMore, ShieldCheck, X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import type { components } from "@/lib/worker-api.gen";

type AttentionItem = components["schemas"]["AttentionItem"];

type AttentionDisplay = { kind?: string; generation?: number; summary?: string; questionCount?: number };

function displayLabel(item: AttentionItem, t: (key: string) => string): string {
  const display = item.display as AttentionDisplay | undefined;
  switch (item.kind) {
    case "approval_required":
      return display?.kind === "retry_budget" ? t("conversation.attention.retryBudget") : t("conversation.attention.approvalRequired");
    case "needs_input":
      if (display?.kind === "question") {
        const count = display.questionCount ?? 0;
        const key = count === 1 ? "conversation.attention.questionOne" : "conversation.attention.questionMany";
        return t(key).replace("{count}", String(count));
      }
      return t("conversation.attention.needsInput");
    case "delegation_completed":
      return t("conversation.attention.completed");
    case "delegation_failed":
      return t("conversation.attention.failed");
  }
  return t("conversation.attention.title");
}

function actionLabel(actionKind: string | undefined, t: (key: string) => string): string {
  switch (actionKind) {
    case "answer_question": return t("conversation.attention.actionAnswerQuestion");
    case "tool_approval": return t("conversation.attention.actionToolApproval");
    case "delegation_approval": return t("conversation.attention.actionDelegationApproval");
    case "delegation_input": return t("conversation.attention.actionDelegationInput");
    case "none": return t("conversation.attention.action");
  }
  return t("conversation.attention.action");
}

function itemIcon(item: AttentionItem) {
  const props = { size: 13, "aria-hidden": true } as const;
  if (item.kind === "needs_input") return <MessageSquareMore {...props} />;
  if (item.kind === "approval_required") return <ShieldCheck {...props} />;
  if (item.kind === "delegation_failed") return <CircleAlert {...props} />;
  return <CircleAlert {...props} />;
}

// AttentionItemRow renders one attention item with its typed action. Approval
// and input items open their authoritative surface; notifications dismiss.
export function AttentionItemRow({ item, onNavigate, onDismiss }: {
  item: AttentionItem;
  onNavigate: (item: AttentionItem) => void;
  onDismiss: (item: AttentionItem) => void;
}) {
  const t = useT();
  const dismissible = !item.requiresAction;
  return <div className="attention-row" role="listitem" data-kind={item.kind} data-attention-id={item.id}>
    <span className="attention-icon">{itemIcon(item)}</span>
    <button type="button" className="attention-main" onClick={() => onNavigate(item)}>
      <strong>{displayLabel(item, t)}</strong>
      <span>{(item.display as AttentionDisplay | undefined)?.summary ?? item.sessionId}</span>
    </button>
    {dismissible
      ? <button type="button" className="attention-dismiss" aria-label={t("conversation.attention.dismiss")} title={t("conversation.attention.dismiss")}
          onClick={() => onDismiss(item)}><X size={13} aria-hidden="true" /></button>
      : <span className="attention-action-tag">{actionLabel(item.action?.kind, t)}</span>}
  </div>;
}
