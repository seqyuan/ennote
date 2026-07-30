"use client";

import { Check, Monitor, Moon, Sun } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTheme, type ThemeMode } from "@/hooks/useTheme";

const options: Array<{ value: ThemeMode; label: string; Icon: typeof Monitor }> = [
  { value: "system", label: "System theme", Icon: Monitor },
  { value: "light", label: "Light theme", Icon: Sun },
  { value: "dark", label: "Dark theme", Icon: Moon },
];

export function ThemeControl() {
  const { mode, setThemeMode } = useTheme();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const current = options.find((option) => option.value === mode) ?? options[0];

  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    window.addEventListener("pointerdown", close);
    window.addEventListener("keydown", escape);
    return () => {
      window.removeEventListener("pointerdown", close);
      window.removeEventListener("keydown", escape);
    };
  }, [open]);

  return (
    <div className="theme-control" ref={root}>
      <button type="button" className="topbar-icon-button" aria-label="Choose theme" title="Choose theme" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
        <current.Icon size={15} aria-hidden="true" />
      </button>
      {open && (
        <div className="theme-menu" role="menu" aria-label="Theme">
          {options.map(({ value, label, Icon }) => (
            <button
              key={value}
              type="button"
              role="menuitemradio"
              aria-checked={mode === value}
              onClick={(event) => {
                setThemeMode(value, { x: event.clientX, y: event.clientY });
                setOpen(false);
              }}
            >
              <Icon size={15} aria-hidden="true" />
              <span>{label}</span>
              {mode === value && <Check size={14} aria-hidden="true" />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
