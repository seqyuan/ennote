"use client";

import { Bot, FolderPlus, Search, Settings2, Workflow, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import type { Session } from "@/components/settings/types";
import type { SidebarProjectGroup } from "@/hooks/useSidebarProjectGroups";
import { useProjectSelector } from "@/hooks/useProjectSelector";
import { useT } from "@/components/LocaleProvider";
import { SidebarLogoRow } from "./SidebarLogoRow";
import { NewSessionButton } from "./NewSessionButton";
import { NavLink } from "./NavLink";
import { ProjectGroup } from "./ProjectGroup";
import type { WorkspaceView } from "./workspace-view";
import { ProjectSelector } from "./ProjectSelector";

const SCROLLBAR_LINGER_MS = 2000;

interface SidebarProject { id: string; name: string }

interface SessionSidebarProps {
  projects: SidebarProject[];
  groups: SidebarProjectGroup[];
  selectedProject: string | null;
  selectedSession: string | null;
  settingsOpen: boolean;
  query: string;
  setQuery: (value: string) => void;
  mutatingId: string | null;
  announcement: string;
  pinnedProjectIds: string[];
  togglePinProject: (projectId: string) => void;
  renameProject: (projectId: string, name: string) => Promise<void>;
  deleteProject: (projectId: string) => Promise<void>;
  renameSession: (session: Session, title: string) => Promise<void>;
  createSessionIn: (projectId: string) => Promise<void>;
  collapsed: Set<string>;
  toggleCollapsed: (projectId: string) => void;
  archived: Record<string, Session[]>;
  openArchived: (projectId: string) => void;
  createProject: () => void;
  createSession: () => void;
  switchProject: (projectID: string) => void;
  switchSession: (sessionID: string) => void;
  archiveSession: (session: Session) => void;
  restoreSession: (session: Session) => void;
  openSettings: () => void;
  closeNavigation: () => void;
  /** Optional: set of session IDs with active runs for running indicator */
  runningSessionIds?: Set<string>;
  /** Toggle the sidebar column (desktop collapse/expand). */
  onToggleSidebar: () => void;
  /** Project selector control (owned by AppShell so the empty hero can open it). */
  projectSelector: ReturnType<typeof useProjectSelector>;
  /** Main-area view; Roles/Graphs items switch it without leaving the shell. */
  view: WorkspaceView;
  setView: (view: WorkspaceView) => void;
}

export function SessionSidebar({
  projects, groups, selectedProject, selectedSession, settingsOpen,
  query, setQuery, mutatingId, announcement, pinnedProjectIds, togglePinProject,
  renameProject, deleteProject, renameSession, createSessionIn,
  collapsed, toggleCollapsed, archived, openArchived,
  createProject, createSession, switchProject, switchSession,
  archiveSession, restoreSession, openSettings, closeNavigation, runningSessionIds,
  onToggleSidebar, projectSelector, view, setView,
}: SessionSidebarProps) {
  const t = useT();
  const searchInputRef = useRef<HTMLInputElement>(null);
  const columnRef = useRef<HTMLElement>(null);
  const lingerTimer = useRef<number | undefined>(undefined);
  const [searchExpanded, setSearchExpanded] = useState(false);
  const [pointerInside, setPointerInside] = useState(false);

  useEffect(() => {
    if (searchExpanded) searchInputRef.current?.focus({ preventScroll: true });
  }, [searchExpanded]);

  const cancelLinger = useCallback(() => {
    if (lingerTimer.current === undefined) return;
    window.clearTimeout(lingerTimer.current);
    lingerTimer.current = undefined;
  }, []);
  const armLinger = useCallback(() => {
    if (lingerTimer.current !== undefined) return;
    lingerTimer.current = window.setTimeout(() => {
      lingerTimer.current = undefined;
      setPointerInside(false);
    }, SCROLLBAR_LINGER_MS);
  }, []);

  useEffect(() => {
    if (!pointerInside) return;
    const onMove = (event: PointerEvent) => {
      const rect = columnRef.current?.getBoundingClientRect();
      if (!rect) return;
      const inside = event.clientX >= rect.left && event.clientX < rect.right
        && event.clientY >= rect.top && event.clientY < rect.bottom;
      if (inside) cancelLinger();
      else armLinger();
    };
    document.addEventListener("pointermove", onMove);
    return () => {
      document.removeEventListener("pointermove", onMove);
      cancelLinger();
    };
  }, [pointerInside, cancelLinger, armLinger]);

  useEffect(() => () => cancelLinger(), [cancelLinger]);

  const closeSearch = () => {
    setQuery("");
    setSearchExpanded(false);
  };

  return (
    <aside
      ref={columnRef}
      className={`sidebar${pointerInside ? "" : " quiet-bars"}`}
      aria-label={t("sidebar.aria")}
      onPointerEnter={() => { cancelLinger(); setPointerInside(true); }}
      onPointerLeave={() => { armLinger(); }}
    >
      <SidebarLogoRow
        onToggleSidebar={onToggleSidebar}
        onCloseNavigation={closeNavigation}
        onNewSession={createSession}
        newSessionDisabled={!selectedProject}
      />

      <NewSessionButton disabled={!selectedProject} onClick={createSession} />

      {/* Roles / Graphs below New Session (dsh nav-item style). These switch
          the main-area view inside the shell — the sidebar never navigates away. */}
      <nav className="sidebar-nav" aria-label="Workspace">
        <NavLink active={view === "roles"} label={t("sidebar.roles")} icon={<Bot size={16} />} onClick={() => setView("roles")} />
        <NavLink active={view === "graphs"} label={t("sidebar.graphs")} icon={<Workflow size={16} />} onClick={() => setView("graphs")} />
      </nav>

      {/* Workspace actions sit directly below the primary nav. */}
      <div className={`sidebar-workspace-head${searchExpanded ? " search-expanded" : ""}`}>
        <span className="workspace-label">{t("sidebar.workspaces")}</span>
        <div className="workspace-search-slot">
          <div className="workspace-search">
            <button
              type="button"
              className="workspace-action-button workspace-search-button"
              aria-label={t("sidebar.searchSessions")}
              aria-expanded={searchExpanded}
              title={searchExpanded ? undefined : t("sidebar.searchSessions")}
              onClick={() => setSearchExpanded(true)}
            >
              <Search size={searchExpanded ? 11 : 14} aria-hidden="true" />
            </button>
            <input
              ref={searchInputRef}
              type="text"
              value={query}
              tabIndex={searchExpanded ? 0 : -1}
              placeholder={t("sidebar.searchSessions")}
              aria-label={t("sidebar.searchSessions")}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => { if (event.key === "Escape") closeSearch(); }}
            />
            {searchExpanded && (
              <button type="button" className="workspace-search-clear" aria-label={t("sidebar.clearSearch")} onClick={closeSearch}>
                <X size={13} aria-hidden="true" />
              </button>
            )}
          </div>
        </div>
        <div className="workspace-head-actions">
          <button type="button" className="workspace-action-button" aria-label={t("sidebar.addWorkspace")} title={t("sidebar.addWorkspace")} onClick={createProject}>
            <FolderPlus size={16} aria-hidden="true" />
          </button>
        </div>
      </div>

      {/* Project switching stays above the session list. */}
      <ProjectSelector
        projects={projects}
        selectedProject={selectedProject}
        pinnedProjectIds={pinnedProjectIds}
        togglePinProject={togglePinProject}
        onSelect={switchProject}
        control={projectSelector}
      />

      {selectedProject && (
        <section className="sidebar-sessions" aria-label={t("sidebar.sessions")}>
          <div className="sidebar-tree-body">
            <div className="sidebar-session-list">
              {groups.length === 0 ? (
                <div className="sidebar-empty">{t("sidebar.loadingSessions")}</div>
              ) : (
                groups.map((group) => (
                  <ProjectGroup
                    key={group.projectId}
                    group={group}
                    isCurrent={group.projectId === selectedProject}
                    isPinned={pinnedProjectIds.includes(group.projectId)}
                    isCollapsed={collapsed.has(group.projectId)}
                    onToggleCollapsed={() => toggleCollapsed(group.projectId)}
                    onTogglePin={() => togglePinProject(group.projectId)}
                    query={query.trim()}
                    selectedSession={selectedSession}
                    switchSession={switchSession}
                    archiveSession={archiveSession}
                    restoreSession={restoreSession}
                    renameSession={renameSession}
                    renameProject={renameProject}
                    deleteProject={deleteProject}
                    createSessionIn={createSessionIn}
                    mutatingId={mutatingId}
                    archived={archived[group.projectId] ?? []}
                    onOpenArchived={() => openArchived(group.projectId)}
                    runningSessionIds={runningSessionIds}
                  />
                ))
              )}
            </div>
            <div className="sidebar-list-fade" aria-hidden="true" />
          </div>
        </section>
      )}

      {!selectedProject && (
        <div className="sidebar-empty-project">
          {/* Guidance only: workspace creation lives in the add button above
              the session list — no duplicate New-Project affordance here. */}
          <div>{t("sidebar.chooseProject")}</div>
        </div>
      )}

      <span className="sr-only" role="status" aria-live="polite">{announcement}</span>

      <div className="sidebar-settings">
        <button
          type="button"
          className={`sidebar-settings-trigger${settingsOpen ? " active" : ""}`}
          onClick={() => openSettings()}
          aria-haspopup="dialog"
          aria-expanded={settingsOpen}
        >
          <Settings2 size={16} aria-hidden="true" />
          <span>{t("sidebar.settings")}</span>
        </button>
      </div>
    </aside>
  );
}
