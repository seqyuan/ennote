"use client";

import { useState } from "react";
import { ArrowLeft } from "lucide-react";
import { FileViewer } from "./FileViewer";
import { TabBar } from "./TabBar";
import { RightPanelLanes } from "./RightPanelLanes";
import { CHANNEL_TABS } from "./right-panel/channel-tabs";
import { FilesChannel } from "./right-panel/FilesChannel";
import { GraphsChannel } from "./right-panel/GraphsChannel";
import { StatusChannel } from "./right-panel/StatusChannel";
import type { useRightPanel } from "@/hooks/useRightPanel";
import type { useResizable } from "@/hooks/useResizable";
import type { PanelChannel } from "@/lib/right-panel-channels";
import type { PermissionMode } from "@/lib/permission-mode";

export function RightPanel(props: {
  open: boolean;
  onClose: () => void;
  resize: ReturnType<typeof useResizable>;
  panel: ReturnType<typeof useRightPanel>;
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
    open, onClose, resize, panel, projectId, displayPath, onOpenFile, onPreviewFile,
    selectedSession, sessionTitle, activeRun, inspectorRunId, status, permissionMode,
  } = props;
  const { state, fileTabs } = panel;
  const [navigatorOpen, setNavigatorOpen] = useState(false);
  const toggleNavigator = () => setNavigatorOpen((open) => !open);
  // Picking a channel answers the question the overlay was open for, so it closes.
  const selectChannel = (tabId: string) => {
    panel.setActiveTabId(tabId);
    setNavigatorOpen(false);
  };
  const activeChannelId = state.activeChannelId ?? "files";
  const activeFileTab = fileTabs.find((tab) => tab.id === state.activeInspectorId);

  const channelBody = (
    <>
      {activeChannelId === "files" && (
        <FilesChannel
          projectId={projectId}
          displayPath={displayPath}
          onOpenFile={onOpenFile}
          onPreviewFile={onPreviewFile}
        />
      )}
      {activeChannelId === "graph" && (
        <GraphsChannel sessionId={selectedSession} />
      )}
      {activeChannelId === "tools" && (
        <StatusChannel
          runId={inspectorRunId}
          selectedSession={selectedSession}
          sessionTitle={sessionTitle}
          activeRun={activeRun}
          status={status}
          permissionMode={permissionMode}
        />
      )}
    </>
  );

  const inspectorBody = activeFileTab?.projectId === projectId && activeFileTab.filePath ? (
    // Never render a row the current Project does not own, even if a caller
    // hands the panel a tab list that was not scoped (useRightPanel scopes it).
    <FileViewer projectId={activeFileTab.projectId} filePath={activeFileTab.filePath} fileName={activeFileTab.label} />
  ) : null;

  // The left lane always has a channel; the right lane exists only while files
  // are open. Its strip is therefore the inspector's own, not the merged one.
  const channelStrip = (
    <TabBar
      tabs={CHANNEL_TABS.filter((tab) => state.openChannels.includes(tab.id as PanelChannel))}
      activeTabId={activeChannelId}
      onSelectTab={selectChannel}
      onCloseTab={panel.closeTab}
    />
  );

  const inspectorStrip = fileTabs.length > 0 ? (
    <TabBar
      tabs={fileTabs}
      activeTabId={state.activeInspectorId ?? ""}
      onSelectTab={panel.setActiveTabId}
      onCloseTab={panel.closeTab}
      onReorder={panel.reorderTabs}
    />
  ) : null;

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
      <RightPanelLanes
        width={resize.width}
        channelStrip={channelStrip}
        channelBody={channelBody}
        inspectorStrip={inspectorStrip}
        inspectorBody={inspectorBody}
        narrowStrip={(
          <TabBar
            tabs={[...CHANNEL_TABS, ...fileTabs]}
            activeTabId={panel.activeTabId}
            onSelectTab={selectChannel}
            onCloseTab={panel.closeTab}
            onReorder={panel.reorderTabs}
          />
        )}
        narrowBody={(
          <div className="right-panel-body">
            {panel.activeTabId === state.activeInspectorId && inspectorBody ? inspectorBody : channelBody}
          </div>
        )}
        primaryWidth={state.primaryWidth}
        onResizePrimary={(delta) => panel.resizePrimaryBy(delta, resize.width)}
        navigatorOpen={navigatorOpen}
        onToggleNavigator={toggleNavigator}
      />
    </div>
  );
}

// The summary lives with the channel that draws it; it stays exported here
// because it is the panel's public half for the status render test.
export { RightPanelSummary } from "./right-panel/StatusChannel";
