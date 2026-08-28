"use client";

import { useEffect, useId, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import type { PermissionMode } from "@/lib/permission-mode";

const MODES: PermissionMode[] = ["discuss", "ask", "auto"];

const shieldOutline = "M8.20554 0.899994L14.7901 3.36857V7.01026C14.7901 12 11.0466 14.2103 8.20554 15.3C5.36446 14.2103 1.62012 12 1.62012 7.01026V3.36857L8.20554 0.899994Z";

function PermissionGlyph({ mode }: { mode: PermissionMode }) {
  if (mode === "ask") {
    return (
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden>
        <path d="M8.08887 0.251709C8.20479 0.23085 8.32486 0.241168 8.43652 0.282959L15.0215 2.75171C15.2787 2.84819 15.4492 3.09414 15.4492 3.3689V7.0105C15.4492 7.10986 15.4441 7.2081 15.4414 7.30542C15.0285 7.07175 14.5905 6.87695 14.1309 6.73022V3.82495L8.20508 1.60327L2.2793 3.82495V7.0105C2.27936 9.7171 3.4745 11.5379 5.02734 12.7947C5.01025 12.9942 5 13.1962 5 13.4001C5.00001 13.7617 5.02722 14.1169 5.08008 14.4636C2.91555 13.0393 0.961014 10.752 0.960938 7.0105V3.3689C0.960938 3.09417 1.13146 2.84821 1.38867 2.75171L7.97461 0.282959L8.08887 0.251709Z" fill="currentColor" />
        <path d="M11.3525 5.64688V6.85688H5V5.64688H11.3525Z" fill="currentColor" />
        <path d="M9.5824 8.29376V9.50376H5V8.29376H9.5824Z" fill="currentColor" />
      </svg>
    );
  }
  if (mode === "auto") {
    return (
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden>
        <path d={shieldOutline} stroke="currentColor" strokeWidth="1.31831" strokeLinejoin="round" />
        <path d="M9.10094 4.5V8.75939H7.59888V4.5H9.10094Z" fill="currentColor" />
        <path d="M9.10094 9.8114V11.5H7.59888V9.8114H9.10094Z" fill="currentColor" />
      </svg>
    );
  }
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden>
      <path d={shieldOutline} stroke="currentColor" strokeWidth="1.31831" strokeLinejoin="round" />
      <path d="M12.1654 5.7552L8.9447 9.41475C8.73044 9.65816 8.53628 9.8804 8.35774 10.0423C8.1713 10.2114 7.94235 10.3717 7.64016 10.4254C7.48207 10.4535 7.32 10.4552 7.16151 10.4294C6.85843 10.3801 6.62728 10.2223 6.43836 10.0559C6.25752 9.89653 6.06037 9.67732 5.84264 9.43705L4.72925 8.20897L5.63557 7.38707L6.74897 8.61594C6.98603 8.87755 7.12974 9.03533 7.24673 9.13839C7.31033 9.19443 7.34485 9.21476 7.35823 9.22122C7.38068 9.22484 7.40352 9.22515 7.42593 9.22122C7.40522 9.22502 7.42893 9.23294 7.53583 9.136C7.65132 9.03126 7.79316 8.87139 8.02643 8.60638L11.2479 4.94763L12.1654 5.7552Z" fill="currentColor" />
    </svg>
  );
}

function modeLabel(mode: PermissionMode): string {
  return mode === "discuss" ? "Discuss" : mode === "ask" ? "Ask" : "Auto";
}

export function PermissionChip({
  permissionMode, permissionReady, setPermissionMode, disabled,
}: {
  permissionMode: PermissionMode;
  permissionReady: boolean;
  setPermissionMode: (mode: PermissionMode) => void;
  disabled: boolean;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const id = useId();
  const locked = disabled || !permissionReady;

  const menuOpen = open && !locked;

  useEffect(() => {
    if (!menuOpen) return;
    const close = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [menuOpen]);

  return (
    <div ref={rootRef} className="permission-chip">
      <button
        type="button"
        className="permission-chip-trigger"
        aria-label={`${t("composer.permissionMode")}: ${modeLabel(permissionMode)}`}
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        aria-controls={menuOpen ? `${id}-menu` : undefined}
        disabled={locked}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="permission-chip-icon" aria-hidden><PermissionGlyph mode={permissionMode} /></span>
        <span className="permission-chip-label">{modeLabel(permissionMode)}</span>
        <svg className={menuOpen ? "permission-chip-chevron open" : "permission-chip-chevron"} width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M3.5 5.25L7 8.75L10.5 5.25" />
        </svg>
      </button>
      {menuOpen && (
        <div id={`${id}-menu`} className="permission-chip-menu" role="menu" aria-label={t("composer.permissionMode")}>
          {MODES.map((mode) => {
            const selected = mode === permissionMode;
            return (
              <button
                key={mode}
                type="button"
                role="menuitemradio"
                aria-checked={selected}
                className={selected ? "permission-chip-option selected" : "permission-chip-option"}
                onClick={() => { setPermissionMode(mode); setOpen(false); }}
              >
                <span className="permission-chip-option-icon" aria-hidden><PermissionGlyph mode={mode} /></span>
                <span className="permission-chip-option-label">{modeLabel(mode)}</span>
                <span className="permission-chip-check" aria-hidden>
                  {selected && (
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <polyline points="3 8.5 6.5 12 13 4.5" />
                    </svg>
                  )}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
