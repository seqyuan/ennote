import { useCallback, useState } from "react";
import type { Tab } from "@/components/TabBar";
import { moveTab } from "@/lib/tab-order";
import { resolveActiveTabId, scopeFileTabs } from "@/lib/file-tabs";

/**
 * Right-panel file tabs: the "Files / Graphs / Status" static tabs plus any
 * open file tabs. Owns the tab list and the active tab id; opening a tab from
 * the file tree also toggling the right panel is a layout concern handled by
 * the caller (AppShell wraps `openFile` to set rightPanelOpen(true)).
 *
 * Open rows are parked for every project that opened one, but only the current
 * project's rows are returned: a project switch therefore cannot show the
 * previous project's file and falls back to the Files panel.
 */
export function useFileTabs(currentProjectId: string | null): {
  fileTabs: Tab[];
  activeTabId: string;
  setActiveTabId: (id: string) => void;
  openFile: (path: string, name: string) => void;
  closeTab: (id: string) => void;
  reorderTabs: (fromId: string, toId: string) => void;
} {
  const [openTabs, setOpenTabs] = useState<Tab[]>([]);
  const [requestedTabId, setRequestedTabId] = useState<string>("files");

  const fileTabs = scopeFileTabs(openTabs, currentProjectId);
  const activeTabId = resolveActiveTabId(requestedTabId, fileTabs.map((tab) => tab.id));

  const openFile = useCallback((filePath: string, fileName: string) => {
    if (!currentProjectId) return;
    const tabId = `file:${currentProjectId}:${filePath}`;
    setOpenTabs((previous) => {
      if (previous.find((tab) => tab.id === tabId)) return previous;
      return [...previous, { id: tabId, label: fileName, filePath, projectId: currentProjectId }];
    });
    setRequestedTabId(tabId);
  }, [currentProjectId]);

  const closeTab = useCallback((tabId: string) => {
    if (tabId === "files" || tabId === "tools") return;
    setOpenTabs((previous) => previous.filter((tab) => tab.id !== tabId));
    setRequestedTabId((current) => {
      if (current !== tabId) return current;
      const remaining = scopeFileTabs(openTabs, currentProjectId).filter((tab) => tab.id !== tabId);
      return remaining.length > 0 ? remaining[remaining.length - 1].id : "files";
    });
  }, [openTabs, currentProjectId]);

  const reorderTabs = useCallback((fromId: string, toId: string) => {
    setOpenTabs((previous) => moveTab(previous, fromId, toId));
  }, []);

  return { fileTabs, activeTabId, setActiveTabId: setRequestedTabId, openFile, closeTab, reorderTabs };
}
