"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { Tab } from "@/components/TabBar";
import { moveTab } from "@/lib/tab-order";
import { scopeFileTabs } from "@/lib/file-tabs";
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
  preferencesOf,
  rightPanelPreferencesKey,
  serializePreferences,
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

  const hydratedFor = useRef<string | null>(null);
  const lastWritten = useRef<string | null>(null);

  // A Project switch restores that Project's channels, but it does not expand
  // or collapse the shell: the user's decision about the shell outlives the
  // project they were looking at. Only the first hydration reads the stored flag.
  useEffect(() => {
    if (hydratedFor.current === projectId) return;
    const first = hydratedFor.current === null;
    hydratedFor.current = projectId;
    const preferences = projectId ? readPreferences(projectId) : emptyPreferences("");
    setState((current) => {
      const next = initialRightPanelState(preferences);
      return first ? next : { ...next, open: current.open };
    });
  }, [projectId]);

  // Only the restoring facts are written, and only when they change. The state
  // carries its own Project, so the render that still holds the previous
  // Project's selection cannot write it under the new Project's key.
  useEffect(() => {
    if (!projectId || state.projectId !== projectId) return;
    const serialized = serializePreferences(preferencesOf(state));
    if (lastWritten.current === serialized) return;
    lastWritten.current = serialized;
    try {
      window.localStorage.setItem(rightPanelPreferencesKey(projectId), serialized);
    } catch { /* storage unavailable: the panel still works for this visit */ }
  }, [projectId, state]);

  const fileTabs = useMemo(() => scopeFileTabs(openTabs, projectId), [openTabs, projectId]);
  const scopedIds = useMemo(() => fileTabs.map((tab) => tab.id), [fileTabs]);

  // The strip highlights whichever lane the user touched last. A tab that the
  // current Project does not own is not a candidate, so switching back cannot
  // show the previous Project's file.
  const activeTabId = state.activeLane === "inspector" && state.activeInspectorId !== null
    && scopedIds.includes(state.activeInspectorId)
    ? state.activeInspectorId
    : state.activeChannelId ?? "files";

  const openInspector = useCallback((filePath: string, fileName: string) => {
    if (!projectId) return;
    const id = `file:${projectId}:${filePath}`;
    setOpenTabs((previous) => previous.some((tab) => tab.id === id)
      ? previous
      : [...previous, { id, label: fileName, filePath, projectId }]);
    setState((current) => openInspectorTab(current, id));
  }, [projectId]);

  const setActiveTabId = useCallback((tabId: string) => {
    setState((current) => (isPanelChannel(tabId)
      ? openChannel(current, tabId as PanelChannel)
      : openInspectorTab(current, tabId)));
  }, []);

  const closeTab = useCallback((tabId: string) => {
    // Channels are not closable yet: the strip has no per-channel affordance.
    if (isPanelChannel(tabId)) return;
    setOpenTabs((previous) => previous.filter((tab) => tab.id !== tabId));
    setState((current) => closeInspectorTab(current, tabId, scopedIds));
  }, [scopedIds]);

  const reorderTabs = useCallback((fromId: string, toId: string) => {
    setOpenTabs((previous) => moveTab(previous, fromId, toId));
  }, []);

  const closeChannelTab = useCallback((channel: PanelChannel) => {
    setState((current) => closeChannel(current, channel));
  }, []);

  return {
    fileTabs,
    activeTabId,
    setActiveTabId,
    openInspector,
    closeTab,
    reorderTabs,
    closeChannelTab,
    state,
    open: state.open,
    openShell: () => setState((current) => expandShell(current)),
    closeShell: () => setState((current) => collapseShell(current)),
  };
}

function readPreferences(projectId: string): RightPanelPreferencesV1 {
  try {
    return parsePreferences(window.localStorage.getItem(rightPanelPreferencesKey(projectId)), projectId);
  } catch {
    return emptyPreferences(projectId);
  }
}
