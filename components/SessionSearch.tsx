"use client";

import { Search, X } from "lucide-react";

/** Session search panel: expands from the sidebar header search button. */
export function SessionSearch(props: {
  value: string;
  onChange: (value: string) => void;
  onClear: () => void;
  onEscape: () => void;
}) {
  const { value, onChange, onClear, onEscape } = props;

  return (
    <div className="sidebar-sessions-search" style={{ flexShrink: 0 }}>
      <label className="session-search" style={{ margin: 0 }}>
        <Search size={14} aria-hidden="true" />
        <span className="sr-only">Search sessions</span>
        <input
          type="search"
          autoFocus
          value={value}
          placeholder="Search sessions…"
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={(event) => { if (event.key === "Escape") onEscape(); }}
        />
        <button
          type="button"
          onClick={onClear}
          aria-label="Clear search"
          style={{ display: "grid", placeItems: "center", width: 18, height: 18, padding: 0, border: 0, background: "transparent", color: "var(--text-dim)", cursor: "pointer" }}
        >
          <X size={13} />
        </button>
      </label>
    </div>
  );
}
