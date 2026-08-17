"use client";

import { useEffect, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";

/**
 * Shared rename dialog (deepseek-harness renameInput: 44px tall, 22px radius)
 * used by both the project row and the session row. Caller owns open state and
 * commits through onConfirm; Enter saves, Escape cancels.
 */
export function SidebarRenameDialog({ title, initialName, onCancel, onConfirm }: {
  title: string;
  initialName: string;
  onCancel: () => void;
  onConfirm: (name: string) => Promise<void>;
}) {
  const [draft, setDraft] = useState(initialName);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const t = useT();
  useEffect(() => { inputRef.current?.select(); }, []);
  const trimmed = draft.trim();
  const blocked = busy || trimmed === "" || trimmed === initialName;
  const confirm = async () => {
    if (blocked) return;
    setBusy(true);
    setError(null);
    try {
      await onConfirm(trimmed);
    } catch (reason) {
      setError((reason as Error).message);
      setBusy(false);
    }
  };
  return (
    <div className="sb-dialog-overlay" onPointerDown={(e) => { if (e.target === e.currentTarget && !busy) onCancel(); }}>
      <div className="sb-dialog" role="dialog" aria-modal="true" aria-label={title}>
        <h3>{title}</h3>
        <input
          ref={inputRef}
          className="sb-dialog-input"
          value={draft}
          disabled={busy}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void confirm();
            if (e.key === "Escape") onCancel();
          }}
        />
        {error && <div className="sb-dialog-error" role="alert">{error}</div>}
        <div className="sb-dialog-actions">
          <button type="button" onClick={onCancel} disabled={busy}>{t("sidebar.cancel")}</button>
          <button type="button" className="sb-primary" onClick={confirm} disabled={blocked}>{t("sidebar.save")}</button>
        </div>
      </div>
    </div>
  );
}
