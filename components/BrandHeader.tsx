"use client";

import { Plus, Search, X } from "lucide-react";

export function BrandHeader(props: {
  createDisabled: boolean;
  onNewChat: () => void;
  searchOpen: boolean;
  onToggleSearch: () => void;
  onCloseNavigation: () => void;
}) {
  const { createDisabled, onNewChat, searchOpen, onToggleSearch, onCloseNavigation } = props;
  const selectedProject = !createDisabled;

  return (
    <div
      style={{
        padding: "12px 10px 10px",
        borderBottom: "1px solid var(--border)",
        flexShrink: 0,
      }}
    >
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
          <span className="brand-mark">E</span>
          <strong style={{ fontSize: 13 }}>Ennote</strong>
        </div>
        <div style={{ display: "flex", gap: 6 }}>
          <button
            onClick={onNewChat}
            disabled={createDisabled}
            title={selectedProject ? "New chat" : "Select a project first"}
            style={{
              display: "flex", alignItems: "center", justifyContent: "center", gap: 5,
              background: "var(--bg-hover)",
              border: "1px solid var(--border)",
              color: "var(--text-muted)", cursor: selectedProject ? "pointer" : "not-allowed",
              height: 32, paddingLeft: 10, paddingRight: 12,
              borderRadius: 7, fontSize: 12, fontWeight: 500, opacity: selectedProject ? 1 : 0.5,
              flexShrink: 0, transition: "background 0.12s, color 0.12s, border-color 0.12s",
            }}
            onMouseEnter={(e) => {
              if (!selectedProject) return;
              e.currentTarget.style.background = "var(--bg-selected)";
              e.currentTarget.style.color = "var(--accent)";
              e.currentTarget.style.borderColor = "rgba(37,99,235,0.35)";
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.background = "var(--bg-hover)";
              e.currentTarget.style.color = "var(--text-muted)";
              e.currentTarget.style.borderColor = "var(--border)";
            }}
          >
            <Plus size={12} />
            New Chat
          </button>
          <button
            type="button"
            onClick={onToggleSearch}
            disabled={createDisabled}
            aria-label="Search sessions"
            aria-expanded={searchOpen}
            title={selectedProject ? "Search sessions" : "Select a project first"}
            style={{
              display: "flex", alignItems: "center", justifyContent: "center",
              background: searchOpen ? "var(--bg-selected)" : "var(--bg-hover)",
              border: "1px solid var(--border)",
              color: searchOpen ? "var(--accent)" : "var(--text-muted)",
              cursor: selectedProject ? "pointer" : "not-allowed",
              width: 32, height: 32, opacity: selectedProject ? 1 : 0.5,
              borderRadius: 7, padding: 0, flexShrink: 0,
              transition: "background 0.12s, color 0.12s, border-color 0.12s",
            }}
            onMouseEnter={(e) => {
              if (!selectedProject) return;
              e.currentTarget.style.background = "var(--bg-selected)";
              e.currentTarget.style.color = "var(--accent)";
              e.currentTarget.style.borderColor = "rgba(37,99,235,0.35)";
            }}
            onMouseLeave={(e) => {
              if (!selectedProject || searchOpen) return;
              e.currentTarget.style.background = "var(--bg-hover)";
              e.currentTarget.style.color = "var(--text-muted)";
              e.currentTarget.style.borderColor = "var(--border)";
            }}
          >
            <Search size={15} aria-hidden="true" />
          </button>
          <button
            onClick={onCloseNavigation}
            className="icon-btn navigation-close"
            aria-label="Close navigation"
            title="Close navigation"
          >
            <X size={15} />
          </button>
        </div>
      </div>
    </div>
  );
}
