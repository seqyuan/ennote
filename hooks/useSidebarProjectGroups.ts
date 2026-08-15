"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Session } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

export interface SidebarProjectGroup {
  projectId: string;
  projectName: string;
  sessions: Session[];
  loading: boolean;
  error: string | null;
}

const COLLAPSED_KEY = "ennote:sidebar:collapsed-projects";
const REFRESH_INTERVAL_MS = 30_000;

function loadCollapsed(): Set<string> {
  if (typeof window === "undefined") return new Set();
  try {
    const raw = window.localStorage.getItem(COLLAPSED_KEY);
    return raw ? new Set(JSON.parse(raw) as string[]) : new Set();
  } catch {
    return new Set();
  }
}

function saveCollapsed(collapsed: Set<string>) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...collapsed]));
  } catch { /* storage unavailable */ }
}

/**
 * Loads the session list for every sidebar group (the current project plus any
 * pinned projects), annovibe-style: each group shows its own active sessions
 * laid out flat. Archived sessions are fetched lazily via openArchived() so the
 * sidebar stays cheap for the common path.
 */
export function useSidebarProjectGroups(
  projects: { id: string; name: string }[],
  pinnedProjectIds: string[],
  currentProjectId: string | null,
) {
  const [groups, setGroups] = useState<Record<string, SidebarProjectGroup>>({});
  const [archived, setArchived] = useState<Record<string, Session[]>>({});
  const [collapsed, setCollapsed] = useState<Set<string>>(loadCollapsed);
  const generation = useRef(0);

  const groupIds = useMemo(() => {
    const ids: string[] = [];
    const add = (id: string | null | undefined) => { if (id && !ids.includes(id)) ids.push(id); };
    add(currentProjectId);
    for (const id of pinnedProjectIds) add(id);
    return ids;
  }, [currentProjectId, pinnedProjectIds]);

  const loadProject = useCallback(async (projectId: string) => {
    const version = ++generation.current;
    const project = projects.find((p) => p.id === projectId);
    const projectName = project?.name ?? projectId;
    setGroups((cur) => ({ ...cur, [projectId]: { projectId, projectName, sessions: [], loading: true, error: null } }));
    try {
      const sessions = await apiFetch<Session[]>(`/v1/projects/${encodeURIComponent(projectId)}/sessions?status=active`);
      if (generation.current !== version) return;
      setGroups((cur) => ({ ...cur, [projectId]: { projectId, projectName, sessions, loading: false, error: null } }));
    } catch (reason) {
      if (generation.current !== version) return;
      setGroups((cur) => ({ ...cur, [projectId]: { projectId, projectName, sessions: [], loading: false, error: (reason as Error).message } }));
    }
  }, [projects]);

  // Load missing groups and keep the current group fresh on project switch.
  useEffect(() => {
    for (const id of groupIds) {
      if (!groups[id]) void loadProject(id);
    }
  }, [groupIds, groups, loadProject]);

  useEffect(() => {
    if (!currentProjectId) return;
    void loadProject(currentProjectId);
  }, [currentProjectId, loadProject]);

  // Periodic refresh for external changes (CLI sessions, metadata updates).
  useEffect(() => {
    const timer = window.setInterval(() => {
      for (const id of groupIds) void loadProject(id);
    }, REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [groupIds, loadProject]);

  const refresh = useCallback((projectId: string) => void loadProject(projectId), [loadProject]);
  const refreshAll = useCallback(() => { for (const id of groupIds) void loadProject(id); }, [groupIds, loadProject]);

  const openArchived = useCallback(async (projectId: string) => {
    if (archived[projectId]) return;
    try {
      const sessions = await apiFetch<Session[]>(`/v1/projects/${encodeURIComponent(projectId)}/sessions?status=archived`);
      setArchived((cur) => ({ ...cur, [projectId]: sessions }));
    } catch {
      setArchived((cur) => ({ ...cur, [projectId]: [] }));
    }
  }, [archived]);

  const refreshArchived = useCallback(async (projectId: string) => {
    try {
      const sessions = await apiFetch<Session[]>(`/v1/projects/${encodeURIComponent(projectId)}/sessions?status=archived`);
      setArchived((cur) => ({ ...cur, [projectId]: sessions }));
    } catch {
      setArchived((cur) => ({ ...cur, [projectId]: [] }));
    }
  }, []);

  const toggleCollapsed = useCallback((projectId: string) => {
    setCollapsed((cur) => {
      const next = new Set(cur);
      if (next.has(projectId)) next.delete(projectId); else next.add(projectId);
      saveCollapsed(next);
      return next;
    });
  }, []);

  const orderedGroups = useMemo(
    () => groupIds.map((id) => groups[id]).filter((g): g is SidebarProjectGroup => Boolean(g)),
    [groupIds, groups],
  );

  return {
    groups: orderedGroups,
    archived,
    collapsed,
    toggleCollapsed,
    refresh,
    refreshAll,
    openArchived,
    refreshArchived,
  };
}
