"use client";

import { useCallback, useEffect } from "react";
import type { Tab } from "@/components/TabBar";
import { useFileTabs } from "@/hooks/useFileTabs";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type ProjectWorkspace = components["schemas"]["ProjectWorkspace"];

/**
 * Project file operations for the chat shell: resolves the project-scoped
 * workspace (host paths are display-only), surfaces workspace load errors
 * in the run error slot, and owns the right-panel file tabs plus the
 * open/preview affordances.
 */
export function useProjectFiles({
  projectId,
  workspaceFor,
  setRunError,
  onOpenPanel,
  onPreviewFile,
}: {
  projectId: string | null;
  workspaceFor: (projectId: string) => ProjectWorkspace | undefined;
  setRunError: (message: string) => void;
  onOpenPanel: () => void;
  onPreviewFile: (file: { projectId: string; path: string; name: string }) => void;
}) {
  const currentWorkspace = projectId ? workspaceFor(projectId) ?? null : null;
  const currentCwd = currentWorkspace?.hostPath ?? null;

  // Workspace loading is owned by the WorkspaceProvider; surface load errors here.
  useEffect(() => {
    if (!projectId || workspaceFor(projectId)) return;
    const controller = new AbortController();
    void apiFetch<ProjectWorkspace>(`/v1/projects/${encodeURIComponent(projectId)}/workspace`, { signal: controller.signal })
      .catch((reason) => {
        if (!controller.signal.aborted) setRunError((reason as Error).message);
      });
    return () => controller.abort();
    // workspaceFor is stable via context; re-run on project switch.
  }, [projectId, setRunError, workspaceFor]);

  const fileTabsState = useFileTabs(projectId);
  const { fileTabs, activeTabId: activeRightTabId, setActiveTabId: setActiveRightTabId, openFile, closeTab } = fileTabsState;

  const handleOpenFile = useCallback((filePath: string, fileName: string) => {
    openFile(filePath, fileName);
    onOpenPanel();
  }, [openFile, onOpenPanel]);

  const handlePreviewFile = useCallback((filePath: string, fileName: string) => {
    if (!projectId) return;
    onPreviewFile({ projectId, path: filePath, name: fileName });
  }, [projectId, onPreviewFile]);

  // Right tabs: built-in views first, then open file tabs.
  const rightTabs: Tab[] = [
    { id: "files", label: "Files", closable: false, icon: "files" },
    { id: "graph", label: "Graphs", closable: false, icon: "graph" },
    { id: "tools", label: "Status", closable: false, icon: "tools" },
    ...fileTabs,
  ];

  return {
    currentWorkspace,
    currentCwd,
    fileTabs,
    rightTabs,
    activeRightTabId,
    setActiveRightTabId,
    handleOpenFile,
    handlePreviewFile,
    closeTab,
  };
}
