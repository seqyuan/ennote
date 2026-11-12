"use client";

import { useState } from "react";
import { ArrowLeft, Pin, PinOff, Plus } from "lucide-react";

import { ArtifactCanvasTab } from "./ArtifactCanvasTab";
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
import { PRIMARY_MIN_WIDTH, SPLIT_PANE_MIN_WIDTH, laneMode, resolvePrimaryLaneWidth } from "@/lib/right-panel-width";

export function RightPanel(props: {
  open: boolean;
  onClose: () => void;
  resize: ReturnType<typeof useResizable>;
  panel: ReturnType<typeof useRightPanel>;
  projectId: string | null;
  displayPath: string | null;
  onOpenFile: (path: string, name: string) => void;
  onOpenFileToSide: (path: string, name: string) => void;
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
    open, onClose, resize, panel, projectId, displayPath, onOpenFile, onOpenFileToSide, onPreviewFile,
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

  // Two panes need room for two readable files — measured in the inspector lane,
  // which shares the panel with the tree, not in the panel. When the lane is too
  // narrow but would be wide enough with the tree at its floor, the offer stands
  // and opening gives the tree's room away.
  const treeWidth = navigatorOpen && activeChannelId !== null
    ? resolvePrimaryLaneWidth(state.primaryWidths[activeChannelId], resize.width)
    : 0;
  const laneWidth = resize.width - treeWidth - (treeWidth > 0 ? 8 : 0);
  const splitNeeds = SPLIT_PANE_MIN_WIDTH * 2 + 8;
  const laneWithTreeAtFloor = resize.width - PRIMARY_MIN_WIDTH - 8;
  const canSplit = !medium && laneMode(resize.width, drawer) === "wide"
    && (laneWidth >= splitNeeds || laneWithTreeAtFloor >= splitNeeds);
  const openToSide = (path: string, name: string) => {
    if (laneWidth < splitNeeds && treeWidth > PRIMARY_MIN_WIDTH) {
      // One adjustment, only when asked for: the comparison is the point, and a
      // tree at its floor is still usable.
      panel.resizePrimaryBy(PRIMARY_MIN_WIDTH - treeWidth, resize.width);
    }
    onOpenFileToSide(path, name);
    if (medium) setPeek(false);
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
          onOpenFileToSide={openToSide}
          canOpenToSide={canSplit}
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

  // Each pane draws its own selection. A row the current Project does not own is
  // never rendered, even if a caller hands the panel an unscoped list.
  // A row is only ever found in its own pane's list: searching the union is how
  // one file ends up drawn twice after a stale selection.
  const bodyFor = (tabId: string | null, tabs: typeof fileTabs) => {
    const tab = tabs.find((candidate) => candidate.id === tabId);
    if (!tab?.projectId || tab.projectId !== projectId) return null;
    // An artifact row carries no path, so it must be branched on before the
    // path check: keeping the old `!tab.filePath` bail first would leave every
    // artifact tab permanently blank. The key is the tab id, so switching from
    // one artifact to another remounts instead of showing the previous one's
    // bytes while the next fetch is in flight.
    if (tab.artifact) {
      return <ArtifactCanvasTab
        key={tab.id}
        sessionId={tab.artifact.sessionId}
        artifactId={tab.artifact.artifactId}
        name={tab.label}
      />;
    }
    if (!tab.filePath) return null;
    return <FileViewer projectId={tab.projectId} filePath={tab.filePath} fileName={tab.label} />;
  };
  const mainBody = bodyFor(panel.activePanes.main, panel.paneTabs.main);
  const sideBody = bodyFor(panel.activePanes.side, panel.paneTabs.side);
  const split = canSplit && panel.paneTabs.side.length > 0;
  const focusedBody = bodyFor(panel.activePanes[panel.focusedPane], panel.paneTabs[panel.focusedPane]);
  // The pane wrapper is always the container, split or not: turning the side pane
  // on must not move the main pane to a different place in the tree, or React
  // remounts the viewer and the reader loses the line they were on.
  const paneBodies = split
    ? [mainBody, sideBody]
    // Rows exist for the side pane but there is no room: show the one the reader
    // is in, so nothing they opened becomes unreachable.
    : [panel.paneTabs.side.length > 0 ? focusedBody : mainBody];
  const inspectorBody = fileTabs.length > 0 ? (
    <div className={`right-panel-panes${split ? " is-split" : ""}`}>
      {paneBodies.map((body, index) => (
        <div
          key={index === 0 ? "main" : "side"}
          className="right-panel-pane"
          data-testid={split
            ? (index === 0 ? "inspector-pane-main" : "inspector-pane-side")
            : "inspector-pane-focused"}
        >
          {body}
        </div>
      ))}
    </div>
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
        inspectorStrip={split ? (
          <div className="right-panel-pane-strips">
            <TabBar
              tabs={panel.paneTabs.main}
              activeTabId={panel.activePanes.main ?? ""}
              onSelectTab={selectTab}
              onCloseTab={panel.closeTab}
              onReorder={panel.reorderTabs}
              closeLabel={closeLabel}
            />
            <TabBar
              tabs={panel.paneTabs.side}
              activeTabId={panel.activePanes.side ?? ""}
              onSelectTab={selectTab}
              onCloseTab={panel.closeTab}
              onReorder={panel.reorderTabs}
              closeLabel={closeLabel}
            />
          </div>
        ) : fileTabs.length > 0 ? (
          <TabBar
            tabs={fileTabs}
            activeTabId={panel.activeInspectorId ?? ""}
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
            {panel.activeTabId === panel.activeInspectorId && focusedBody ? focusedBody : channelBody}
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
