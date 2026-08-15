"use client";

import { ArrowLeft } from "lucide-react";
import { FileTreePanel } from "./FileTreePanel";
import { FileViewer } from "./FileViewer";
import { GraphActivityPanel } from "./GraphActivityPanel";
import { TabBar, type Tab } from "./TabBar";
import type { useResizable } from "@/hooks/useResizable";
import type { PermissionMode } from "@/lib/permission-mode";

export function RightPanel(props: {
  open: boolean;
  onClose: () => void;
  resize: ReturnType<typeof useResizable>;
  tabs: Tab[];
  activeTabId: string;
  onSelectTab: (id: string) => void;
  onCloseTab: (id: string) => void;
  projectId: string | null;
  displayPath: string | null;
  onOpenFile: (path: string, name: string) => void;
  onPreviewFile: (path: string, name: string) => void;
  selectedSession: string | null;
  sessionTitle: string;
  activeRun: string | null;
  status: string;
  permissionMode: PermissionMode;
}) {
  const {
    open, onClose, resize, tabs, activeTabId, onSelectTab, onCloseTab,
    projectId, displayPath, onOpenFile, onPreviewFile,
    selectedSession, sessionTitle, activeRun, status, permissionMode,
  } = props;
  const activeFileTab = tabs.find((t) => t.id === activeTabId);

  return (
    <div
      className={`right-panel-container${open ? " right-panel-open" : " right-panel-closed"}`}
      style={{
        background: "var(--bg)",
        borderLeft: "1px solid var(--border)",
        display: "flex",
        flexDirection: "column",
        width: resize.width,
        minWidth: resize.width,
        transition: resize.isResizing ? "none" : undefined,
      }}
    >
      <button type="button" className="right-panel-back-button" onClick={onClose}>
        <ArrowLeft size={15} aria-hidden="true" />
        Back to conversation
      </button>
      <TabBar
        tabs={tabs}
        activeTabId={activeTabId}
        onSelectTab={onSelectTab}
        onCloseTab={onCloseTab}
      />
      <div style={{ flex: 1, minHeight: 0, overflow: "hidden" }}>
        {activeTabId === "files" && (
          <FileTreePanel
            key={projectId ?? "no-project"}
            projectId={projectId}
            displayPath={displayPath}
            onOpenFile={onOpenFile}
            onPreviewFile={onPreviewFile}
          />
        )}
        {activeTabId === "graph" && (
          <GraphActivityPanel sessionId={selectedSession} />
        )}
        {activeTabId === "tools" && (
          <div style={{ padding: 18, color: "var(--text-muted)", fontSize: 13 }}>
            <div style={{ fontWeight: 600, marginBottom: 12 }}>Session Status</div>
            <div style={{ fontSize: 12 }}>
              {selectedSession ? (
                <>
                  <div>Session: {sessionTitle}</div>
                  <div>Active run: {activeRun || "none"}</div>
                  <div>Status: {status || "idle"}</div>
                  <div>Permission: {permissionMode}</div>
                </>
              ) : (
                <div>No session selected</div>
              )}
            </div>
          </div>
        )}
        {activeFileTab?.projectId && activeFileTab.filePath && (
          <FileViewer projectId={activeFileTab.projectId} filePath={activeFileTab.filePath} fileName={activeFileTab.label} />
        )}
      </div>
    </div>
  );
}
