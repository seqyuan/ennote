"use client";

import { CircleAlert, MessageSquareMore, ShieldCheck, X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import type { components } from "@/lib/worker-api.gen";

type AttentionItem = components["schemas"]["AttentionItem"];

function displayLabel(item: AttentionItem, t: (key: string) => string): string {
  const display = item.display as { kind?: string; generation?: number; summary?: string } | undefined;
  switch (item.kind) {
    case "approval_required":
      return display?.kind === "retry_budget" ? t("conversation.attention.retryBudget") : t("conversation.attention.approvalRequired");
    case "needs_input":
      return t("conversation.attention.needsInput");
    case "delegation_completed":
      return t("conversation.attention.completed");
    case "delegation_failed":
      return t("conversation.attention.failed");
  }
  return t("conversation.attention.title");
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
      <span>{(item.display as { summary?: string } | undefined)?.summary ?? item.sessionId}</span>
    </button>
    {dismissible
      ? <button type="button" className="attention-dismiss" aria-label={t("conversation.attention.dismiss")} title={t("conversation.attention.dismiss")}
          onClick={() => onDismiss(item)}><X size={13} aria-hidden="true" /></button>
      : <span className="attention-action-tag">{item.action?.kind ?? t("conversation.attention.action")}</span>}
  </div>;
}
