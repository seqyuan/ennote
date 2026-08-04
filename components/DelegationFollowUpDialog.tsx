"use client";

import { MessageSquareMore, Send, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type DelegationGeneration = components["schemas"]["DelegationGeneration"];

// DelegationFollowUpDialog submits a typed continuation command (input or
// follow-up) for one item. The text draft survives stale responses: on
// conflict the dialog refreshes the group instead of discarding input.
export function DelegationFollowUpDialog({ itemID, itemName, kind, expectedGeneration, onDone }: {
  itemID: string;
  itemName: string;
  kind: "input" | "follow_up";
  expectedGeneration: number;
  onDone: () => void;
}) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const command = kind === "input" ? "input" : "follow-up";
  const submit = async () => {
    if (busy || !text.trim()) return;
    setBusy(true);
    setError(null);
    const draft = text;
    try {
      await apiFetch<{ generation: DelegationGeneration }>(
        `/v1/delegation-items/${encodeURIComponent(itemID)}/${command}`,
        { method: "POST", body: JSON.stringify({
          expectedGeneration, text: draft, clientRequestId: crypto.randomUUID(),
        }) },
      );
      onDone();
    } catch (reason) {
      // Stale generation: refresh authoritative state, keep the draft.
      setError((reason as Error).message);
      onDone();
      setText(draft);
    } finally {
      setBusy(false);
    }
  };

  return <div className="follow-up-dialog" role="dialog" aria-label={`${kind === "input" ? "Reply" : "Follow up"} ${itemName}`}>
    <div className="follow-up-header">
      <span><MessageSquareMore size={14} aria-hidden="true" />
        {kind === "input" ? "Reply to delegated task" : "Private follow-up"}</span>
      <button type="button" className="follow-up-close" aria-label="Close" title="Close"
        onClick={onDone}><X size={14} aria-hidden="true" /></button>
    </div>
    <div className="follow-up-meta">
      <strong>{itemName}</strong>
      <span>generation {expectedGeneration} → {expectedGeneration + 1}</span>
    </div>
    <textarea ref={inputRef} className="follow-up-input" rows={3}
      placeholder={kind === "input" ? "Answer the question the delegated task asked…" : "Additional private instruction…"}
      value={text} onChange={event => setText(event.target.value)} />
    {error && <div className="follow-up-error" role="status">Refreshed: {error}</div>}
    <div className="follow-up-actions">
      <button type="button" className="follow-up-submit" disabled={busy || !text.trim()} onClick={submit}>
        <Send size={13} aria-hidden="true" /> {kind === "input" ? "Send reply" : "Follow up"}
      </button>
    </div>
  </div>;
}
