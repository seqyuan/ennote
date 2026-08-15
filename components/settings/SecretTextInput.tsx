"use client";

import { Eye, EyeOff } from "lucide-react";
import { useState } from "react";

/** annovibe-style secret input: plaintext editable with a reveal toggle. */
export function SecretTextInput({ value, onChange, placeholder }: {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
}) {
  const [visible, setVisible] = useState(false);
  return (
    <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
      <input
        type={visible ? "text" : "password"}
        value={value}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
        style={{
          flex: 1, minWidth: 0, height: 30, padding: "0 8px",
          border: "1px solid var(--border)", borderRadius: 6,
          background: "var(--bg)", color: "var(--text)",
          font: "12px var(--font-mono)",
        }}
      />
      <button
        type="button"
        title={visible ? "Hide key" : "Show key"}
        aria-label={visible ? "Hide key" : "Show key"}
        onClick={() => setVisible((value) => !value)}
        style={{
          display: "grid", placeItems: "center", width: 28, height: 30, padding: 0,
          border: "1px solid var(--border)", borderRadius: 6, background: "var(--bg-panel)",
          color: "var(--text-muted)", cursor: "pointer", flexShrink: 0,
        }}
      >
        {visible ? <EyeOff size={13} aria-hidden="true" /> : <Eye size={13} aria-hidden="true" />}
      </button>
    </span>
  );
}
