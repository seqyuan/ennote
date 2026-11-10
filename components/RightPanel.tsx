"use client";

import { ArrowLeft } from "lucide-react";
import { FileViewer } from "./FileViewer";
import { TabBar, type Tab } from "./TabBar";
import { FilesChannel } from "./right-panel/FilesChannel";
import { GraphsChannel } from "./right-panel/GraphsChannel";
import { StatusChannel } from "./right-panel/StatusChannel";
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
  onReorderTab?: (fromId: string, toId: string) => void;
  projectId: string | null;
  displayPath: string | null;
  onOpenFile: (path: string, name: string) => void;
  onPreviewFile: (path: string, name: string) => void;
  selectedSession: string | null;
  sessionTitle: string;
  activeRun: string | null;
  /** Run the inspector opens: the active Run, or the newest one the timeline has. */
  inspectorRunId: string | null;
  status: string;
  permissionMode: PermissionMode;
}) {
  const {
    open, onClose, resize, tabs, activeTabId, onSelectTab, onCloseTab, onReorderTab,
    projectId, displayPath, onOpenFile, onPreviewFile,
    selectedSession, sessionTitle, activeRun, inspectorRunId, status, permissionMode,
  } = props;
  const activeFileTab = tabs.find((t) => t.id === activeTabId);

  return (
    <div
      className={`right-panel-container${open ? " right-panel-open" : " right-panel-closed"}`}
      style={{
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
        onReorder={onReorderTab}
      />
      <div className="right-panel-body">
        {activeTabId === "files" && (
          <FilesChannel
            projectId={projectId}
            displayPath={displayPath}
            onOpenFile={onOpenFile}
            onPreviewFile={onPreviewFile}
          />
        )}
        {activeTabId === "graph" && (
          <GraphsChannel sessionId={selectedSession} />
        )}
        {activeTabId === "tools" && (
          <StatusChannel
            runId={inspectorRunId}
            selectedSession={selectedSession}
            sessionTitle={sessionTitle}
            activeRun={activeRun}
            status={status}
            permissionMode={permissionMode}
          />
        )}
        {activeFileTab?.projectId === projectId && activeFileTab.filePath && (
          // Never render a row the current Project does not own, even if a caller
          // hands the panel a tab list that was not scoped (useRightPanel scopes it).
          <FileViewer projectId={activeFileTab.projectId} filePath={activeFileTab.filePath} fileName={activeFileTab.label} />
        )}
      </div>
    </div>
  );
}

// The summary lives with the channel that draws it; it stays exported here
// because it is the panel's public half for the status render test.
export { RightPanelSummary } from "./right-panel/StatusChannel";
