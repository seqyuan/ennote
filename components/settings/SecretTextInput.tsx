"use client";

import { Eye, EyeOff } from "lucide-react";
import { useState } from "react";

/** Secret input matching dsh's 32px field, with a reveal toggle. */
export function SecretTextInput({ value, onChange, placeholder, ariaLabel }: {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  ariaLabel?: string;
}) {
  const [visible, setVisible] = useState(false);
  return (
    <span className="settings-models-secret">
      <input
        type={visible ? "text" : "password"}
        value={value}
        placeholder={placeholder}
        aria-label={ariaLabel}
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
      />
      <button
        type="button"
        className="settings-models-secret-toggle"
        title={visible ? "Hide key" : "Show key"}
        aria-label={visible ? "Hide key" : "Show key"}
        onClick={() => setVisible((current) => !current)}
      >
        {visible ? <EyeOff size={13} aria-hidden="true" /> : <Eye size={13} aria-hidden="true" />}
      </button>
    </span>
  );
}
