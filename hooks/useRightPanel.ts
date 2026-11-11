"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { Tab } from "@/components/TabBar";
import { resolvePrimaryLaneWidth as resolvePrimaryLaneWidthOf } from "@/lib/right-panel-width";
import {
  type InspectorPane,
  type PanelChannel,
  type PersistedInspectorTab,
  type RightPanelPreferencesV1,
  type RightPanelState,
  closeChannel,
  closeInspectorTab,
  collapseShell,
  emptyPreferences,
  expandShell,
  initialRightPanelState,
  isPanelChannel,
  openChannel,
  fileTabId,
  openInspectorTab,
  parsePreferences,
  pathOf,
  pinChannel,
  preferencesOf,
  reorderInspectorTabs,
  rightPanelPreferencesKey,
  serializePreferences,
  setNavigatorHidden,
  setPrimaryWidth,
  unpinChannel,
} from "@/lib/right-panel-channels";

/**
 * Where the right panel's tabs are and what they show.
 *
 * The tabs themselves are parked for every Project that opened one, exactly as
 * the file tabs always were: the rows are derived from the selected `projectId`
 * on every render rather than pruned in an effect, so no stale list and no late
 * update from the previous Project can reach the panel. What changes per Project
 * is the *selection* and the shell state, which live in `RightPanelState`.
 *
 * The channel rules are in `lib/right-panel-channels.ts`; this hook only holds
 * the browser-side halves of them: which Project is open, what is on disk, and
 * which tab list the panel should draw.
 */
export function useRightPanel(projectId: string | null) {
  const [state, setState] = useState<RightPanelState>(() =>
    initialRightPanelState(emptyPreferences(projectId ?? "")),
  );

  // The Project whose preferences are already loaded into `state`. Until it
  // matches, neither reading nor writing preferences is safe: the state still
  // describes the previous Project.
  const [hydratedFor, setHydratedFor] = useState<string | null>(null);
  // Bumped when a drag ends, so the settled width is written even though the
  // state it settled on is the one it already had.
  const [writeTick, setWriteTick] = useState(0);
  // The tab each Project was last showing, so leaving and coming back returns
  // the reader to it instead of leaving an empty lane behind.
  const rememberedTab = useRef(new Map<string, string>());
  const lastWritten = useRef<string | null>(null);
  const draggingPrimary = useRef(false);
  // The state of the Project that is being left. A switch replaces the state
  // wholesale, so anything the reader changed in the last commit would go
  // unwritten unless it is flushed here first.
  const outgoing = useRef<RightPanelState | null>(null);
  // Set when the stored preferences could not be read. Writing then would
  // replace real preferences with the defaults this session is running on.
  const storageUnreadable = useRef(false);

  const key = projectId ?? "";

  /**
   * Write one Project's preferences when they changed.
   *
   * Skips a state that does not belong to the Project being shown, a Project
   * whose preferences were never read, a drag in flight, and storage that could
   * not be read in the first place.
   */
  const flush = useCallback((candidate: RightPanelState | null) => {
    if (!candidate || !candidate.projectId) return;
    if (hydratedFor === null || storageUnreadable.current || draggingPrimary.current) return;
    const serialized = serializePreferences(preferencesOf(candidate));
    if (lastWritten.current === serialized) return;
    try {
      window.localStorage.setItem(rightPanelPreferencesKey(candidate.projectId), serialized);
      lastWritten.current = serialized;
    } catch { /* storage unavailable: the panel still works for this visit */ }
  }, [hydratedFor]);


  // Every Project keeps its own preferences, the shell flag included, so a
  // switch applies the incoming Project's stored state as a whole. A null
  // project is the app starting up rather than a project of its own: it hydrates
  // from defaults and writes nothing.
  useEffect(() => {
    if (hydratedFor === key) return;
    flush(outgoing.current);
    const preferences = projectId
      ? readPreferences(projectId, storageUnreadable)
      : emptyPreferences("");
    setState(() => {
      const next = initialRightPanelState(preferences);
      const parked = next.inspectorTabs.map((row) => fileTabId(projectId ?? "", row.path));
      // A switch inside this session wins over what was stored: the reader was
      // just there. Otherwise the stored selection is the one to return to.
      const remembered = rememberedTab.current.get(key);
      const wanted = remembered ?? next.activePanes[next.focusedPane] ?? undefined;
      const restored = wanted !== undefined && parked.includes(wanted) ? wanted : null;
      if (restored === null) {
        return { ...next, activePanes: { main: null, side: null }, activeLane: "channel" };
      }
      const pane = next.inspectorTabs.find((row) => fileTabId(projectId ?? "", row.path) === restored)?.pane ?? "main";
      return {
        ...next,
        activePanes: { ...next.activePanes, [pane]: restored },
        focusedPane: pane,
        activeLane: "inspector",
      };
    });
    setHydratedFor(key);
  }, [key, projectId, hydratedFor, flush]);

  // The strips' rows are the Project's remembered files, per pane.
  const paneTabs = useMemo(() => {
    const rows = state.projectId === projectId ? state.inspectorTabs : [];
    const toTab = (row: PersistedInspectorTab): Tab => ({
      id: fileTabId(projectId ?? "", row.path),
      label: row.name,
      filePath: row.path,
      projectId: projectId ?? undefined,
    });
    return {
      main: rows.filter((row) => (row.pane ?? "main") === "main").map(toTab),
      side: rows.filter((row) => row.pane === "side").map(toTab),
    };
  }, [state.projectId, state.inspectorTabs, projectId]);
  const fileTabs = useMemo(() => [...paneTabs.main, ...paneTabs.side], [paneTabs]);
  const scopedIds = useMemo(() => fileTabs.map((tab) => tab.id), [fileTabs]);

  // The strip highlights whichever lane the user touched last. A tab that the
  // current Project does not own is not a candidate, so switching back cannot
  // show the previous Project's file.
  const focusedSelection = state.activePanes[state.focusedPane];
  const activeInspectorId = state.activeLane === "inspector"
    && focusedSelection !== null
    && scopedIds.includes(focusedSelection)
    ? focusedSelection
    : null;
  const activeChannelId = isPanelChannel(state.activeChannelId)
    && state.openChannels.includes(state.activeChannelId)
    ? state.activeChannelId
    : state.openChannels[0] ?? null;
  const activeTabId = activeInspectorId ?? activeChannelId ?? "files";

  // Only the restoring facts are written, and only when they change. The state
  // carries its own Project, so the render that still holds the previous
  // Project's selection cannot write it under the new Project's key, and
  // nothing is written before the stored preferences have been read back.
  useEffect(() => {
    if (!projectId || state.projectId !== projectId) return;
    if (hydratedFor !== projectId) return;
    // A drag writes once, when it ends: every intermediate width would
    // otherwise cost a synchronous localStorage write per pointer move.
    if (draggingPrimary.current || storageUnreadable.current) return;
    flush(state);
  }, [projectId, state, hydratedFor, writeTick, flush]);

  // Remember the state a commit settled on, so leaving the Project can flush it.
  useEffect(() => {
    outgoing.current = state;
  }, [state]);

  const openInspector = useCallback((filePath: string, fileName: string, pane: InspectorPane = "main") => {
    if (!projectId) return;
    const id = fileTabId(projectId, filePath);
    rememberedTab.current.set(projectId, id);
    setState((current) => openInspectorTab(current, id, { path: filePath, name: fileName, pane }));
  }, [projectId]);

  // Selecting a row only moves the selection; its remembered row is already there.
  const rowOf = (current: RightPanelState, tabId: string) => current.inspectorTabs
    .find((row) => fileTabId(current.projectId, row.path) === tabId)
    ?? { path: pathOf(tabId), name: pathOf(tabId).split("/").filter(Boolean).pop() ?? tabId };

  const setActiveTabId = useCallback((tabId: string) => {
    if (!isPanelChannel(tabId)) rememberedTab.current.set(projectId ?? "", tabId);
    setState((current) => (isPanelChannel(tabId)
      ? openChannel(current, tabId as PanelChannel)
      : openInspectorTab(current, tabId, rowOf(current, tabId))));
  }, [projectId]);

  const closeTab = useCallback((tabId: string) => {
    // Inspector rows only: channels close through `closeChannelTab`, so the
    // strip's own × and this path stay distinct.
    if (isPanelChannel(tabId)) return;
    if (rememberedTab.current.get(projectId ?? "") === tabId) rememberedTab.current.delete(projectId ?? "");
    setState((current) => closeInspectorTab(current, tabId));
  }, [projectId]);

  const reorderTabs = useCallback((fromId: string, toId: string) => {
    setState((current) => reorderInspectorTabs(current, pathOf(fromId), pathOf(toId)));
  }, []);

  const toggleNavigator = useCallback(() => {
    setState((current) => setNavigatorHidden(current, !current.navigatorHidden));
  }, []);

  const closeChannelTab = useCallback((channel: PanelChannel) => {
    setState((current) => closeChannel(current, channel));
  }, []);

  const openChannelTab = useCallback((channel: PanelChannel) => {
    setState((current) => openChannel(current, channel));
  }, []);

  const togglePin = useCallback((channel: PanelChannel) => {
    setState((current) => (current.pinnedChannels.includes(channel)
      ? unpinChannel(current, channel)
      : pinChannel(current, channel)));
  }, []);

  // The divider only changes widths: the lane it resizes stays mounted, and the
  // bound is the one the layout uses, so a drag cannot starve the other lane.
  const resizePrimaryBy = useCallback((delta: number, shellWidth: number) => {
    setState((current) => {
      // The divider resizes the lane of the channel on screen, and each channel
      // keeps its own width: the tree wants room the run inspector does not.
      // Only the channel that is actually drawn has a lane to drag. With the
      // launcher on screen there is no channel, and guessing one would rewrite a
      // width the reader never touched.
      const channel = isPanelChannel(current.activeChannelId) ? current.activeChannelId : null;
      if (channel === null) return current;
      // Start from what the lane is showing, not from what was stored: a width
      // saved on a wider panel would otherwise eat the first Npx of the drag.
      const from = resolvePrimaryLaneWidthOf(current.primaryWidths[channel], shellWidth);
      return setPrimaryWidth(current, channel, resolvePrimaryLaneWidthOf(from + delta, shellWidth));
    });
  }, []);

  const beginPrimaryResize = useCallback(() => { draggingPrimary.current = true; }, []);
  const endPrimaryResize = useCallback(() => {
    draggingPrimary.current = false;
    setWriteTick((tick) => tick + 1);
  }, []);

  return {
    fileTabs,
    paneTabs,
    activePanes: state.activePanes,
    focusedPane: state.focusedPane,
    activeTabId,
    activeInspectorId,
    activeChannelId,
    setActiveTabId,
    openInspector,
    closeTab,
    reorderTabs,
    closeChannelTab,
    toggleNavigator,
    openChannel: openChannelTab,
    togglePin,
    resizePrimaryBy,
    beginPrimaryResize,
    endPrimaryResize,
    state,
    open: state.open,
    openShell: () => setState((current) => expandShell(current)),
    closeShell: () => setState((current) => collapseShell(current)),
  };
}

function readPreferences(projectId: string, unreadable: { current: boolean }): RightPanelPreferencesV1 {
  try {
    return parsePreferences(window.localStorage.getItem(rightPanelPreferencesKey(projectId)), projectId);
  } catch {
    // Nothing can be trusted about what is stored, so this session must not
    // write over it.
    unreadable.current = true;
    return emptyPreferences(projectId);
  }
}
