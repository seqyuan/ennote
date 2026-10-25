"use client";

import { useState } from "react";
import { FileText, Folder, Wrench, Workflow, X } from "lucide-react";
import styles from "./TabBar.module.css";
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
  onReorder?: (fromId: string, toId: string) => void;
}

export function TabBar({ tabs, activeTabId, onSelectTab, onCloseTab, onReorder }: Props) {
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dropTargetId, setDropTargetId] = useState<string | null>(null);
  const resetDrag = () => { setDraggingId(null); setDropTargetId(null); };
  return (
    <div role="tablist" aria-label="Workspace panel tabs" className={styles.bar}>
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
            draggable={Boolean(onReorder)}
            data-dragging={draggingId === tab.id || undefined}
            data-drop-target={dropTargetId === tab.id || undefined}
            onDragStart={(event) => {
              if (!onReorder) return;
              setDraggingId(tab.id);
              event.dataTransfer.effectAllowed = "move";
              event.dataTransfer.setData("text/plain", tab.id);
            }}
            onDragOver={(event) => {
              if (!onReorder || draggingId === null || draggingId === tab.id) return;
              event.preventDefault();
              event.dataTransfer.dropEffect = "move";
              setDropTargetId(tab.id);
            }}
            onDragLeave={() => setDropTargetId((current) => (current === tab.id ? null : current))}
            onDrop={(event) => {
              if (!onReorder) return;
              event.preventDefault();
              const from = event.dataTransfer.getData("text/plain") || draggingId;
              if (from && from !== tab.id) onReorder(from, tab.id);
              resetDrag();
            }}
            onDragEnd={resetDrag}
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
              if (event.altKey && onReorder) {
                onReorder(tab.id, tabs[nextIndex].id);
                return;
              }
              onSelectTab(tabs[nextIndex].id);
              (event.currentTarget.parentElement?.children[nextIndex] as HTMLElement | undefined)?.focus();
            }}
            className={styles.tab}
            data-active={isActive || undefined}
          >
            <span className={styles.icon}>
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
            <span className={styles.label} title={tab.filePath ?? tab.label}>
              {tab.label}
            </span>
            {closable && (
              <button
                type="button"
                className={styles.close}
                onClick={(e) => { e.stopPropagation(); onCloseTab(tab.id); }}
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
