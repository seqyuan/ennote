"use client";

import { useState } from "react";
import { ArrowLeft, Pin, PinOff, Plus } from "lucide-react";

import { FileViewer } from "./FileViewer";
import { TabBar, type Tab } from "./TabBar";
import { RightPanelLanes } from "./RightPanelLanes";
import { channelTabs } from "./right-panel/channel-tabs";
import { ChannelLauncher } from "./right-panel/ChannelLauncher";
import { FilesChannel } from "./right-panel/FilesChannel";
import { GraphsChannel } from "./right-panel/GraphsChannel";
import { StatusChannel } from "./right-panel/StatusChannel";
import { useT } from "@/components/LocaleProvider";
import type { useRightPanel } from "@/hooks/useRightPanel";
import type { useResizable } from "@/hooks/useResizable";
import { PANEL_CHANNELS, isPanelChannel } from "@/lib/right-panel-channels";
import { statusNeedsHuman } from "@/lib/run-status-copy";
import type { PermissionMode } from "@/lib/permission-mode";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { laneMode } from "@/lib/right-panel-width";

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
  // Two levels, because the two layouts mean different things by "the navigator".
  // A wide panel really does have a second column, so hiding it is a standing
  // choice and it is persisted with the rest of the Project's preferences. In a
  // medium panel the navigator is a sheet over the preview, and a sheet is
  // something you open for a moment — it lives for this visit only.
  const drawer = useMediaQuery("(max-width: 960px)");
  const medium = laneMode(resize.width, drawer) === "medium";
  const [peek, setPeek] = useState(false);
  // What each channel has to say about itself. The counts come from the channel
  // that fetched them, so the strip never adds a request of its own.
  // Tagged with the session that reported it: a count from the previous session
  // must not linger on the tab after a switch.
  const [graphRuns, setGraphRuns] = useState<{ session: string | null; count: number } | null>(null);
  const navigatorOpen = medium ? peek : !state.navigatorHidden;
  const toggleNavigator = () => (medium ? setPeek((open) => !open) : panel.toggleNavigator());
  const closePanel = () => {
    setPeek(false);
    onClose();
  };

  const openChannels = state.openChannels;
  const activeChannelId = panel.activeChannelId;

  const closeLabel = (tab: Tab) => t("rightPanel.close", { name: tab.label });
  const closeTab = (tabId: string) => {
    if (isPanelChannel(tabId)) panel.closeChannelTab(tabId);
    else panel.closeTab(tabId);
  };

  /**
   * Hand the strip back to a channel. A peek has served its purpose by then, and
   * picking a channel other than Files ends a hidden tree: the lane it lives in
   * is the only way to reach Graphs or Status.
   */
  const selectTab = (tabId: string) => {
    panel.setActiveTabId(tabId);
    if (medium) setPeek(false);
    if (state.navigatorHidden && tabId !== "files") panel.toggleNavigator();
  };

  const channelBody = (
    <>
      {/* The tree holds the reader's place: expanded directories, scroll, filter.
          It stays mounted while another channel is on screen — and while none is,
          which is when the launcher shows — and only hides. */}
      <div className="right-panel-channel-slot" hidden={activeChannelId !== "files"}>
        <FilesChannel
          projectId={projectId}
          displayPath={displayPath}
          onOpenFile={(path, name) => {
            onOpenFile(path, name);
            // The file is what the reader asked to see, so the sheet gets out of
            // the way. This is not a preference: it costs one click to reopen.
            if (medium) setPeek(false);
          }}
          onPreviewFile={onPreviewFile}
        />
      </div>
      {activeChannelId === "graph" && (
        <GraphsChannel
          sessionId={selectedSession}
          onCount={(count) => setGraphRuns({ session: selectedSession, count })}
        />
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
      {activeChannelId === null && (
        <ChannelLauncher
          channels={PANEL_CHANNELS}
          hint={t("rightPanel.launcherHint")}
          t={t}
          onOpen={panel.openChannel}
        />
      )}
    </>
  );

  // What each channel has to say about itself. The count is reported by the
  // channel that fetched it, so the strip adds no request of its own.
  const signals = {
    graph: { count: graphRuns?.session === selectedSession ? graphRuns.count : undefined },
    tools: { attention: statusNeedsHuman(status) },
  } as const;

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
      <button type="button" className="right-panel-back-button" onClick={closePanel}>
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
              tabs={channelTabs(openChannels, t, signals)}
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
        channelBody={channelBody}
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
              tabs={[...channelTabs(openChannels, t, signals), ...fileTabs]}
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
        primaryWidth={state.primaryWidths[activeChannelId ?? "files"]}
        onResizePrimary={(delta) => panel.resizePrimaryBy(delta, resize.width)}
        onResizePrimaryStart={panel.beginPrimaryResize}
        onResizePrimaryEnd={panel.endPrimaryResize}
        navigatorOpen={navigatorOpen && open}
        primaryHidden={!navigatorOpen && activeChannelId === "files"}
        onToggleNavigator={toggleNavigator}
      />
    </div>
  );
}

// The summary lives with the channel that draws it; it stays exported here
// because it is the panel's public half for the status render test.
export { RightPanelSummary } from "./right-panel/StatusChannel";
