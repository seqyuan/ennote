"use client";

import { useCallback, useEffect, useRef, useState, createContext, useContext, type ReactNode } from "react";
import type { components } from "@/lib/worker-api.gen";
import { apiFetch } from "@/lib/worker-api.client";

type Project = components["schemas"]["Project"];
type ProjectWorkspace = components["schemas"]["ProjectWorkspace"];

const PINNED_KEY = "ennote-pinned-projects";

interface WorkspaceValue {
  projects: Project[];
  selectedProject: string | null;
  switchProject: (projectId: string) => void;
  createProjectOpen: boolean;
  openCreateProject: () => void;
  confirmCreateProject: (name: string, hostPath: string) => Promise<void>;
  cancelCreateProject: () => void;
  createProjectBusy: boolean;
  settingsOpen: boolean;
  openSettings: () => void;
  closeSettings: () => void;
  pinnedProjectIds: string[];
  togglePinProject: (projectId: string) => void;
  workspaceFor: (projectId: string) => ProjectWorkspace | undefined;
}

const WorkspaceContext = createContext<WorkspaceValue | null>(null);

export function useWorkspace(): WorkspaceValue {
  const value = useContext(WorkspaceContext);
  if (!value) throw new Error("useWorkspace must be used inside <WorkspaceProvider>");
  return value;
}

/**
 * WorkspaceProvider owns the cross-page project navigation state shared by the
 * chat shell (/), the Roles page (/roles), and the Graphs page (/graphs):
 * project catalog + selection, project creation dialog, settings dialog
 * visibility, and the pinned-project list (localStorage).
 *
 * Session selection, session trees, and chat state stay in AppShell.
 */
export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [selectedProject, setSelectedProject] = useState<string | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [creatingProject, setCreatingProject] = useState(false);
  const [creatingProjectBusy, setCreatingProjectBusy] = useState(false);
  const [workspaceMap, setWorkspaceMap] = useState<Map<string, ProjectWorkspace>>(new Map());
  const [pinnedProjectIds, setPinnedProjectIds] = useState<string[]>(() => {
    try {
      const raw = localStorage.getItem(PINNED_KEY);
      const parsed: unknown = raw ? JSON.parse(raw) : [];
      return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === "string") : [];
    } catch {
      return [];
    }
  });

  useEffect(() => {
    apiFetch<Project[]>("/v1/projects").then(setProjects).catch(() => {});
  }, []);

  const switchProject = useCallback((projectId: string) => {
    setSettingsOpen(false);
    setSelectedProject(projectId);
  }, []);

  const openSettings = useCallback(() => setSettingsOpen(true), []);
  const closeSettings = useCallback(() => setSettingsOpen(false), []);

  const openCreateProject = useCallback(() => {
    setCreatingProjectBusy(false);
    setCreatingProject(true);
  }, []);

  const confirmCreateProject = useCallback(async (name: string, hostPath: string) => {
    setCreatingProjectBusy(true);
    try {
      const result = await apiFetch<{ project: Project; workspace: ProjectWorkspace }>("/v1/projects", {
        method: "POST", body: JSON.stringify({ name, hostPath }),
      });
      setCreatingProject(false);
      if (result.workspace) {
        setWorkspaceMap((previous) => new Map(previous).set(result.project.id, result.workspace));
      }
      setProjects(await apiFetch<Project[]>("/v1/projects"));
      switchProject(result.project.id);
    } finally {
      setCreatingProjectBusy(false);
    }
  }, [switchProject]);

  const cancelCreateProject = useCallback(() => {
    if (creatingProjectBusy) return;
    setCreatingProject(false);
  }, [creatingProjectBusy]);

  const togglePinProject = useCallback((projectId: string) => {
    setPinnedProjectIds((current) => {
      const next = current.includes(projectId)
        ? current.filter((id) => id !== projectId)
        : [...current, projectId];
      try {
        localStorage.setItem(PINNED_KEY, JSON.stringify(next));
      } catch {
        /* localStorage unavailable: keep in-memory only */
      }
      return next;
    });
  }, []);

  const workspaceFor = useCallback((projectId: string) => workspaceMap.get(projectId), [workspaceMap]);

  const value: WorkspaceValue = {
    projects, selectedProject, switchProject,
    createProjectOpen: creatingProject, openCreateProject, confirmCreateProject, cancelCreateProject,
    createProjectBusy: creatingProjectBusy,
    settingsOpen, openSettings, closeSettings,
    pinnedProjectIds, togglePinProject, workspaceFor,
  };
  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>;
}

// Re-export for consumers that only need the loading hook.
export const pinnedProjectsStorageKey = PINNED_KEY;
