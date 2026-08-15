import { useCallback, useState } from "react";
import type { Tab } from "@/components/TabBar";

/**
 * Right-panel file tabs: the "Files / Graphs / Status" static tabs plus any
 * open file tabs. Owns the tab list and the active tab id; opening a tab from
 * the file tree also toggling the right panel is a layout concern handled by
 * the caller (AppShell wraps `openFile` to set rightPanelOpen(true)).
 */
export function useFileTabs(currentProjectId: string | null): {
  fileTabs: Tab[];
  activeTabId: string;
  setActiveTabId: (id: string) => void;
  openFile: (path: string, name: string) => void;
  closeTab: (id: string) => void;
} {
  const [fileTabs, setFileTabs] = useState<Tab[]>([]);
  const [activeTabId, setActiveTabId] = useState<string>("files");

  const openFile = useCallback((filePath: string, fileName: string) => {
    if (!currentProjectId) return;
    const tabId = `file:${currentProjectId}:${filePath}`;
    setFileTabs((previous) => {
      if (previous.find((tab) => tab.id === tabId)) return previous;
      return [...previous, { id: tabId, label: fileName, filePath, projectId: currentProjectId }];
    });
    setActiveTabId(tabId);
  }, [currentProjectId]);

  const closeTab = useCallback((tabId: string) => {
    if (tabId === "files" || tabId === "tools") return;
    setFileTabs((prev) => prev.filter((t) => t.id !== tabId));
    setActiveTabId((cur) => {
      if (cur !== tabId) return cur;
      const remaining = fileTabs.filter((t) => t.id !== tabId);
      return remaining.length > 0 ? remaining[remaining.length - 1].id : "files";
    });
  }, [fileTabs]);

  return { fileTabs, activeTabId, setActiveTabId, openFile, closeTab };
}
