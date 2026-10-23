"use client";

import { Minimize2, X } from "lucide-react";
import { useEffect, useRef } from "react";
import { useT } from "@/components/LocaleProvider";

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
  const t = useT();
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  return <div className="compaction-prompt-bar" role="dialog" aria-label={t("conversation.compaction.aria")}>
    <div className="compaction-prompt-header">
      <span><Minimize2 size={13} aria-hidden="true" /> {t("conversation.compaction.title")}</span>
      <button type="button" className="follow-up-close" aria-label={t("conversation.compaction.cancel")} title={t("conversation.compaction.cancel")} onClick={onCancel} disabled={busy}>
        <X size={13} aria-hidden="true" />
      </button>
    </div>
    <div className="compaction-prompt-body">
      <input
        ref={inputRef}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={t("conversation.compaction.placeholder")}
        disabled={busy}
        onKeyDown={(event) => {
          if (event.key === "Enter" && !busy) {
            event.preventDefault();
            onConfirm();
          }
        }}
      />
      <div className="compaction-prompt-actions">
        <button type="button" className="secondary-btn" onClick={onCancel} disabled={busy}>{t("conversation.compaction.cancel")}</button>
        <button type="button" className="compaction-prompt-confirm" onClick={onConfirm} disabled={busy}>
          {busy ? t("conversation.compaction.creating") : t("conversation.compaction.confirm")}
        </button>
      </div>
    </div>
  </div>;
}
