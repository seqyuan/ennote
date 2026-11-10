"use client";

import { useState } from "react";
import { ArrowLeft, Pin, PinOff, Plus } from "lucide-react";

import { FileViewer } from "./FileViewer";
import { TabBar, type Tab } from "./TabBar";
import { RightPanelLanes } from "./RightPanelLanes";
import { channelLabel, channelTabs } from "./right-panel/channel-tabs";
import { ChannelLauncher } from "./right-panel/ChannelLauncher";
import { FilesChannel } from "./right-panel/FilesChannel";
import { GraphsChannel } from "./right-panel/GraphsChannel";
import { StatusChannel } from "./right-panel/StatusChannel";
import { useT } from "@/components/LocaleProvider";
import type { useRightPanel } from "@/hooks/useRightPanel";
import type { useResizable } from "@/hooks/useResizable";
import { PANEL_CHANNELS, isPanelChannel, type PanelChannel } from "@/lib/right-panel-channels";
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
  const t = useT();
  const { state, fileTabs } = panel;
  const [navigatorOpen, setNavigatorOpen] = useState(false);
  const toggleNavigator = () => setNavigatorOpen((open) => !open);

  const openChannels = state.openChannels;
  const activeChannelId = openChannels.includes(state.activeChannelId as PanelChannel)
    ? state.activeChannelId as PanelChannel
    : openChannels[0] ?? null;

  const closeLabel = (tab: Tab) => t("rightPanel.close", { name: tab.label });
  const closeTab = (tabId: string) => {
    if (isPanelChannel(tabId)) panel.closeChannelTab(tabId);
    else panel.closeTab(tabId);
  };

  // A channel's strip pick closes the navigator it was opened from.
  const selectTab = (tabId: string) => {
    panel.setActiveTabId(tabId);
    setNavigatorOpen(false);
  };

  const channelBody = activeChannelId === null ? null : (
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

  const activeFileTab = fileTabs.find((tab) => tab.id === state.activeInspectorId);
  const inspectorBody = activeFileTab?.projectId === projectId && activeFileTab.filePath ? (
    // Never render a row the current Project does not own, even if a caller
    // hands the panel a tab list that was not scoped (useRightPanel scopes it).
    <FileViewer projectId={activeFileTab.projectId} filePath={activeFileTab.filePath} fileName={activeFileTab.label} />
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
        /* The left lane always draws its own strip: the channels it shows, a pin
           for the selected one, and the way back to the channels it is not. */
        channelStrip={(
          <div className="right-panel-strip">
            <TabBar
              tabs={channelTabs(openChannels, t)}
              activeTabId={activeChannelId ?? ""}
              onSelectTab={selectTab}
              onCloseTab={closeTab}
              closeLabel={closeLabel}
            />
            {activeChannelId !== null && (
              <button
                type="button"
                className="right-panel-strip-action"
                aria-pressed={state.pinnedChannels.includes(activeChannelId)}
                aria-label={state.pinnedChannels.includes(activeChannelId)
                  ? t("rightPanel.unpin")
                  : t("rightPanel.pin")}
                title={state.pinnedChannels.includes(activeChannelId)
                  ? t("rightPanel.unpin")
                  : t("rightPanel.pin")}
                onClick={() => panel.togglePin(activeChannelId)}
              >
                {state.pinnedChannels.includes(activeChannelId)
                  ? <Pin size={13} aria-hidden="true" />
                  : <PinOff size={13} aria-hidden="true" />}
              </button>
            )}
            {openChannels.length < PANEL_CHANNELS.length && (
              <button
                type="button"
                className="right-panel-strip-action"
                aria-label={t("rightPanel.add")}
                title={t("rightPanel.add")}
                onClick={() => {
                  const next = PANEL_CHANNELS.find((channel) => !openChannels.includes(channel));
                  if (next) panel.openChannel(next);
                }}
              >
                <Plus size={13} aria-hidden="true" />
              </button>
            )}
          </div>
        )}
        channelBody={channelBody ?? (
          <ChannelLauncher
            channels={PANEL_CHANNELS}
            hint={t("rightPanel.launcherHint")}
            t={t}
            onOpen={panel.openChannel}
          />
        )}
        inspectorStrip={fileTabs.length > 0 ? (
          <TabBar
            tabs={fileTabs}
            activeTabId={state.activeInspectorId ?? ""}
            onSelectTab={selectTab}
            onCloseTab={panel.closeTab}
            onReorder={panel.reorderTabs}
            closeLabel={closeLabel}
          />
        ) : null}
        inspectorBody={inspectorBody}
        narrowStrip={(
          <div className="right-panel-strip">
            <TabBar
              tabs={[...channelTabs(openChannels, t), ...fileTabs]}
              activeTabId={panel.activeTabId}
              onSelectTab={selectTab}
              onCloseTab={closeTab}
              onReorder={panel.reorderTabs}
              closeLabel={closeLabel}
            />
          </div>
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

/** Kept for callers that still import the label helper from here. */
export { channelLabel };
