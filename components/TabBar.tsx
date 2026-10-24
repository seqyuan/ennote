"use client";

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
}

export function TabBar({ tabs, activeTabId, onSelectTab, onCloseTab }: Props) {
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
