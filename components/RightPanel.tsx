"use client";

import { ArrowLeft } from "lucide-react";
import { FileTreePanel } from "./FileTreePanel";
import { FileViewer } from "./FileViewer";
import { GraphActivityPanel } from "./GraphActivityPanel";
import { RunInspectorPanel } from "./RunInspectorPanel";
import { TabBar, type Tab } from "./TabBar";
import { useRunMCP } from "@/hooks/useRunMCP";
import { useRunTranscript } from "@/hooks/useRunTranscript";
import { useRunPromptComposition } from "@/hooks/useRunPromptComposition";
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
  /** Run the inspector opens: the active Run, or the newest one the timeline has. */
  inspectorRunId: string | null;
  status: string;
  permissionMode: PermissionMode;
}) {
  const {
    open, onClose, resize, tabs, activeTabId, onSelectTab, onCloseTab,
    projectId, displayPath, onOpenFile, onPreviewFile,
    selectedSession, sessionTitle, activeRun, inspectorRunId, status, permissionMode,
  } = props;
  const activeFileTab = tabs.find((t) => t.id === activeTabId);
  // One fetch per open inspector; the hooks clear themselves with no target.
  const inspectedRunId = activeTabId === "tools" ? inspectorRunId : null;
  const prompt = useRunPromptComposition(inspectedRunId);
  const mcp = useRunMCP(inspectedRunId);
  const transcript = useRunTranscript(inspectedRunId);

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
          <div style={{ height: "100%", display: "flex", flexDirection: "column", minHeight: 0 }}>
            <RunInspectorPanel
              runId={inspectorRunId}
              composition={prompt.composition}
              mcp={mcp.frozen}
              transcript={transcript}
              loading={prompt.loading}
              error={prompt.error}
            />
            <div style={{ marginTop: "auto", padding: "0 16px 12px", fontSize: 11, color: "var(--text-dim)" }}>
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
