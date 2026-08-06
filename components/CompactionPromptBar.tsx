"use client";

import { Minimize2, X } from "lucide-react";
import { useEffect, useRef } from "react";

// CompactionPromptBar is the inline confirmation for a manual context
// checkpoint. It replaces the previous native prompt() and keeps the optional
// focus instruction on the conversation surface.
export function CompactionPromptBar({ value, onChange, busy, onConfirm, onCancel }: {
  value: string;
  onChange: (value: string) => void;
  busy: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  return <div className="compaction-prompt-bar" role="dialog" aria-label="Create context checkpoint">
    <div className="compaction-prompt-header">
      <span><Minimize2 size={13} aria-hidden="true" /> Create context checkpoint</span>
      <button type="button" className="follow-up-close" aria-label="Cancel" title="Cancel" onClick={onCancel} disabled={busy}>
        <X size={13} aria-hidden="true" />
      </button>
    </div>
    <div className="compaction-prompt-body">
      <input
        ref={inputRef}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder="Optional focus for the checkpoint…"
        disabled={busy}
        onKeyDown={(event) => {
          if (event.key === "Enter" && !busy) {
            event.preventDefault();
            onConfirm();
          }
        }}
      />
      <div className="compaction-prompt-actions">
        <button type="button" className="secondary-btn" onClick={onCancel} disabled={busy}>Cancel</button>
        <button type="button" className="compaction-prompt-confirm" onClick={onConfirm} disabled={busy}>
          {busy ? "Creating…" : "Create checkpoint"}
        </button>
      </div>
    </div>
  </div>;
}
