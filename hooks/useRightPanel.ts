"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { Tab } from "@/components/TabBar";
import { moveTab } from "@/lib/tab-order";
import { scopeFileTabs } from "@/lib/file-tabs";
import { resolvePrimaryLaneWidth as resolvePrimaryLaneWidthOf } from "@/lib/right-panel-width";
import {
  type PanelChannel,
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
  openInspectorTab,
  parsePreferences,
  pinChannel,
  preferencesOf,
  rightPanelPreferencesKey,
  serializePreferences,
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
  const [openTabs, setOpenTabs] = useState<Tab[]>([]);
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
  // Set when the stored preferences could not be read. Writing then would
  // replace real preferences with the defaults this session is running on.
  const storageUnreadable = useRef(false);

  const key = projectId ?? "";

  // Every Project keeps its own preferences, the shell flag included, so a
  // switch applies the incoming Project's stored state as a whole. A null
  // project is the app starting up rather than a project of its own: it hydrates
  // from defaults and writes nothing.
  useEffect(() => {
    if (hydratedFor === key) return;
    const preferences = projectId
      ? readPreferences(projectId, storageUnreadable)
      : emptyPreferences("");
    setState(() => {
      const next = initialRightPanelState(preferences);
      const parked = openTabs
        .filter((tab) => tab.projectId === projectId)
        .map((tab) => tab.id);
      const remembered = rememberedTab.current.get(key);
      const restored = remembered !== undefined && parked.includes(remembered) ? remembered : null;
      return {
        ...next,
        open: next.open,
        activeInspectorId: restored,
        activeLane: restored === null ? "channel" : "inspector",
      };
    });
    setHydratedFor(key);
  }, [key, projectId, hydratedFor, openTabs]);

  const fileTabs = useMemo(() => scopeFileTabs(openTabs, projectId), [openTabs, projectId]);
  const scopedIds = useMemo(() => fileTabs.map((tab) => tab.id), [fileTabs]);

  // The strip highlights whichever lane the user touched last. A tab that the
  // current Project does not own is not a candidate, so switching back cannot
  // show the previous Project's file.
  const activeInspectorId = state.activeLane === "inspector"
    && state.activeInspectorId !== null
    && scopedIds.includes(state.activeInspectorId)
    ? state.activeInspectorId
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
    const serialized = serializePreferences(preferencesOf(state));
    if (lastWritten.current === serialized) return;
    try {
      window.localStorage.setItem(rightPanelPreferencesKey(projectId), serialized);
      lastWritten.current = serialized;
    } catch { /* storage unavailable: the panel still works for this visit */ }
  }, [projectId, state, hydratedFor, writeTick]);

  const openInspector = useCallback((filePath: string, fileName: string) => {
    if (!projectId) return;
    const id = `file:${projectId}:${filePath}`;
    setOpenTabs((previous) => previous.some((tab) => tab.id === id)
      ? previous
      : [...previous, { id, label: fileName, filePath, projectId }]);
    rememberedTab.current.set(projectId, id);
    setState((current) => openInspectorTab(current, id));
  }, [projectId]);

  const setActiveTabId = useCallback((tabId: string) => {
    if (!isPanelChannel(tabId)) rememberedTab.current.set(projectId ?? "", tabId);
    setState((current) => (isPanelChannel(tabId)
      ? openChannel(current, tabId as PanelChannel)
      : openInspectorTab(current, tabId)));
  }, [projectId]);

  const closeTab = useCallback((tabId: string) => {
    // Inspector rows only: channels close through `closeChannelTab`, so the
    // strip's own × and this path stay distinct.
    if (isPanelChannel(tabId)) return;
    setOpenTabs((previous) => previous.filter((tab) => tab.id !== tabId));
    if (rememberedTab.current.get(projectId ?? "") === tabId) rememberedTab.current.delete(projectId ?? "");
    setState((current) => closeInspectorTab(current, tabId, scopedIds));
  }, [projectId, scopedIds]);

  const reorderTabs = useCallback((fromId: string, toId: string) => {
    setOpenTabs((previous) => moveTab(previous, fromId, toId));
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
      // Start from what the lane is showing, not from what was stored: a width
      // saved on a wider panel would otherwise eat the first Npx of the drag.
      const from = resolvePrimaryLaneWidthOf(current.primaryWidth, shellWidth);
      return setPrimaryWidth(current, resolvePrimaryLaneWidthOf(from + delta, shellWidth));
    });
  }, []);

  const beginPrimaryResize = useCallback(() => { draggingPrimary.current = true; }, []);
  const endPrimaryResize = useCallback(() => {
    draggingPrimary.current = false;
    setWriteTick((tick) => tick + 1);
  }, []);

  return {
    fileTabs,
    activeTabId,
    activeInspectorId,
    activeChannelId,
    setActiveTabId,
    openInspector,
    closeTab,
    reorderTabs,
    closeChannelTab,
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
