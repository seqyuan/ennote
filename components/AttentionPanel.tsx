"use client";

import { Bell } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { AttentionItemRow } from "@/components/AttentionItemRow";
import { useAttention } from "@/hooks/useAttention";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type AttentionItem = components["schemas"]["AttentionItem"];

// AttentionPanel is the global cross-session bell. It groups action-required
// items ahead of notifications without reordering their source, navigates to
// the exact source session on selection, and dismisses notifications only.
export function AttentionPanel({ projectId, onNavigate }: {
  projectId: string | undefined;
  onNavigate: (item: AttentionItem) => void;
}) {
  const { items, pendingCount, refresh } = useAttention(projectId);
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", escape);
    };
  }, [open]);

  const dismiss = async (item: AttentionItem) => {
    try {
      await apiFetch(`/v1/attention/${encodeURIComponent(item.id)}/dismiss`, {
        method: "POST",
        body: JSON.stringify({ clientRequestId: crypto.randomUUID() }),
      });
      refresh();
    } catch {
      refresh();
    }
  };

  const actionFirst = [...items].sort((a, b) =>
    (b.requiresAction ? 1 : 0) - (a.requiresAction ? 1 : 0));

  return <div className="attention-panel" ref={containerRef}>
    <button type="button" className="attention-bell" aria-label={`Attention (${pendingCount})`}
      title="Attention" onClick={() => setOpen(value => !value)}>
      <Bell size={15} aria-hidden="true" />
      {pendingCount > 0 && <span className="attention-badge">{pendingCount > 99 ? "99+" : pendingCount}</span>}
    </button>
    {open && <div className="attention-popover" role="dialog" aria-label="Attention">
      <div className="attention-popover-header">
        <strong>Attention</strong>
        <span>{pendingCount} pending</span>
      </div>
      <div className="attention-list" role="list">
        {actionFirst.length === 0 && <div className="attention-empty">Nothing needs attention.</div>}
        {actionFirst.map(item => <AttentionItemRow key={item.id} item={item}
          onNavigate={item => { setOpen(false); onNavigate(item); }}
          onDismiss={dismiss} />)}
      </div>
    </div>}
  </div>;
}
