"use client";

import { Bot, FolderPlus, Plus, Search, Settings2, Workflow, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
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
  collapsed, toggleCollapsed, archived, openArchived,
  createProject, createSession, switchProject, switchSession,
  archiveSession, restoreSession, openSettings, closeNavigation, runningSessionIds,
  onToggleSidebar, projectSelector, view, setView,
}: SessionSidebarProps) {
  const t = useT();
  const searchInputRef = useRef<HTMLInputElement>(null);
  const [searchExpanded, setSearchExpanded] = useState(false);

  useEffect(() => {
    if (searchExpanded) searchInputRef.current?.focus({ preventScroll: true });
  }, [searchExpanded]);

  const closeSearch = () => {
    setQuery("");
    setSearchExpanded(false);
  };

  return (
    <aside className="sidebar" aria-label={t("sidebar.aria")}>
      <SidebarLogoRow
        onToggleSidebar={onToggleSidebar}
        onCloseNavigation={closeNavigation}
      />

      <NewSessionButton disabled={!selectedProject} onClick={createSession} />

      {/* Roles / Graphs below New Session (dsh nav-item style). These switch
          the main-area view inside the shell — the sidebar never navigates away. */}
      <nav style={{ flexShrink: 0, padding: "0 0 8px", display: "flex", flexDirection: "column", gap: 2 }} aria-label="Workspace">
        <NavLink active={view === "roles"} label={t("sidebar.roles")} icon={<Bot size={15} />} onClick={() => setView("roles")} />
        <NavLink active={view === "graphs"} label={t("sidebar.graphs")} icon={<Workflow size={15} />} onClick={() => setView("graphs")} />
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
        onCreate={createProject}
        control={projectSelector}
      />

      {selectedProject && (
        <section className="sidebar-section sidebar-sessions" aria-label={t("sidebar.sessions")} style={{ flex: "1 1 auto", minHeight: 0, display: "flex", flexDirection: "column" }}>
          <div style={{ flex: "1 1 auto", overflowY: "auto", minHeight: 0, padding: "0 6px 8px" }}>
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
                  mutatingId={mutatingId}
                  archived={archived[group.projectId] ?? []}
                  onOpenArchived={() => openArchived(group.projectId)}
                  runningSessionIds={runningSessionIds}
                />
              ))
            )}
          </div>
        </section>
      )}

      {!selectedProject && (
        <div style={{ flex: "1 1 auto", display: "flex", alignItems: "center", justifyContent: "center", padding: 18 }}>
          <div style={{ textAlign: "center", color: "var(--text-dim)", fontSize: 12 }}>
            <div>{t("sidebar.chooseProject")}</div>
            <button onClick={createProject} style={{ marginTop: 8, padding: "3px 8px", border: "none", borderRadius: 5, background: "transparent", color: "var(--text-dim)", cursor: "pointer", fontSize: 12 }}>
              <Plus size={11} style={{ display: "inline", verticalAlign: "middle", marginRight: 4 }} />
              {t("sidebar.newProject")}
            </button>
          </div>
        </div>
      )}

      <span className="sr-only" role="status" aria-live="polite">{announcement}</span>

      {/* Settings at bottom (icon in rail) */}
      <div className="sidebar-section sidebar-settings" style={{ flexShrink: 0 }}>
        <button
          className={`sidebar-item sidebar-button ${settingsOpen ? "active" : ""}`}
          onClick={openSettings}
          aria-haspopup="dialog"
          aria-expanded={settingsOpen}
        >
          <Settings2 size={15} aria-hidden="true" />
          <span>{t("sidebar.settings")}</span>
        </button>
      </div>
    </aside>
  );
}
