"use client";

import { CircleAlert, MessageSquareMore, ShieldCheck, X } from "lucide-react";
import type { components } from "@/lib/worker-api.gen";

type AttentionItem = components["schemas"]["AttentionItem"];

function displayLabel(item: AttentionItem): string {
  const display = item.display as { kind?: string; generation?: number; summary?: string } | undefined;
  switch (item.kind) {
    case "approval_required":
      return display?.kind === "retry_budget" ? "Retry budget increase awaits approval" : "Approval required";
    case "needs_input":
      return "Delegated task needs your input";
    case "delegation_completed":
      return "Background delegation completed";
    case "delegation_failed":
      return "Background delegation failed";
  }
  return "Attention";
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
  const dismissible = !item.requiresAction;
  return <div className="attention-row" role="listitem" data-kind={item.kind} data-attention-id={item.id}>
    <span className="attention-icon">{itemIcon(item)}</span>
    <button type="button" className="attention-main" onClick={() => onNavigate(item)}>
      <strong>{displayLabel(item)}</strong>
      <span>{(item.display as { summary?: string } | undefined)?.summary ?? item.sessionId}</span>
    </button>
    {dismissible
      ? <button type="button" className="attention-dismiss" aria-label="Dismiss" title="Dismiss"
          onClick={() => onDismiss(item)}><X size={13} aria-hidden="true" /></button>
      : <span className="attention-action-tag">{item.action?.kind ?? "action"}</span>}
  </div>;
}
