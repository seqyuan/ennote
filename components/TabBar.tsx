"use client";

import { FileText, Folder, Wrench, Workflow, X } from "lucide-react";
import { useState } from "react";
import { getFileIcon } from "./FileIcons";

export interface Tab {
  id: string;
  label: string;
  filePath?: string;
  projectId?: string;
  closable?: boolean;
  icon?: "report" | "tools" | "files" | "graph";
}

interface Props {
  tabs: Tab[];
  activeTabId: string;
  onSelectTab: (id: string) => void;
  onCloseTab: (id: string) => void;
}

export function TabBar({ tabs, activeTabId, onSelectTab, onCloseTab }: Props) {
  const [hoveredClose, setHoveredClose] = useState<string | null>(null);

  return (
    <div
      role="tablist"
      aria-label="Workspace panel tabs"
      style={{
        display: "flex",
        alignItems: "flex-end",
        background: "var(--bg-panel)",
        overflowX: "auto",
        flexShrink: 0,
        height: 36,
      }}
    >
      {tabs.map((tab, index) => {
        const isActive = tab.id === activeTabId;
        const closable = tab.closable !== false;
        return (
          <div
            key={tab.id}
            role="tab"
            tabIndex={isActive ? 0 : -1}
            aria-selected={isActive}
            onClick={() => onSelectTab(tab.id)}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                onSelectTab(tab.id);
                return;
              }
              if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
              event.preventDefault();
              const offset = event.key === "ArrowRight" ? 1 : -1;
              const nextIndex = (index + offset + tabs.length) % tabs.length;
              onSelectTab(tabs[nextIndex].id);
              (event.currentTarget.parentElement?.children[nextIndex] as HTMLElement | undefined)?.focus();
            }}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              height: 36,
              paddingLeft: 12,
              paddingRight: 6,
              borderRight: "1px solid var(--border)",
              background: isActive ? "var(--bg)" : "var(--bg-panel)",
              cursor: "pointer",
              fontSize: 12,
              color: isActive ? "var(--text)" : "var(--text-muted)",
              whiteSpace: "nowrap",
              maxWidth: 180,
              minWidth: 80,
              flexShrink: 0,
              userSelect: "none",
              transition: "background 0.1s, color 0.1s",
            }}
          >
            <span style={{ flexShrink: 0, opacity: isActive ? 1 : 0.7, display: "flex", alignItems: "center" }}>
              {tab.icon === "files" ? (
                <Folder size={13} aria-hidden="true" />
              ) : tab.icon === "report" ? (
                <FileText size={13} aria-hidden="true" />
              ) : tab.icon === "tools" ? (
                <Wrench size={13} aria-hidden="true" />
              ) : tab.icon === "graph" ? (
                <Workflow size={13} aria-hidden="true" />
              ) : (
                getFileIcon(tab.filePath ?? tab.label, 13)
              )}
            </span>
            <span
              style={{
                overflow: "hidden",
                textOverflow: "ellipsis",
                flex: 1,
                fontWeight: isActive ? 500 : 400,
              }}
              title={tab.filePath ?? tab.label}
            >
              {tab.label}
            </span>
            {closable && (
              <button
                onClick={(e) => { e.stopPropagation(); onCloseTab(tab.id); }}
                onMouseEnter={() => setHoveredClose(tab.id)}
                onMouseLeave={() => setHoveredClose(null)}
                style={{
                  display: "flex", alignItems: "center", justifyContent: "center",
                  width: 16, height: 16,
                  background: hoveredClose === tab.id ? "var(--bg-hover)" : "transparent",
                  border: "none",
                  borderRadius: "var(--radius-xs)",
                  color: hoveredClose === tab.id ? "var(--text)" : "var(--text-dim)",
                  cursor: "pointer",
                  padding: 0,
                  flexShrink: 0,
                  transition: "background 0.1s, color 0.1s",
                }}
                title="Close"
                aria-label={`Close ${tab.label}`}
              >
                <X size={10} aria-hidden="true" />
              </button>
            )}
          </div>
        );
      })}
    </div>
  );
}
