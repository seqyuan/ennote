"use client";

import { Bot, Plus, RefreshCw, Search, Settings2, Workflow, X } from "lucide-react";
import { useEffect, useRef } from "react";
import type { Session } from "@/components/settings/types";
import type { SidebarProjectGroup } from "@/hooks/useSidebarProjectGroups";
import { useProjectSelector } from "@/hooks/useProjectSelector";
import { useT } from "@/components/LocaleProvider";
import { SidebarLogoRow } from "./SidebarLogoRow";
import { NewSessionButton } from "./NewSessionButton";
import { NavLink } from "./NavLink";
import { ProjectGroup } from "./ProjectGroup";
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
  refreshGroups: () => void;
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
  /** Desktop rail mode (true when the column is collapsed to 56px). */
  railMode: boolean;
  /** Toggle the sidebar column (desktop collapse/expand). */
  onToggleSidebar: () => void;
}

export function SessionSidebar({
  projects, groups, selectedProject, selectedSession, settingsOpen,
  query, setQuery, mutatingId, announcement, pinnedProjectIds, togglePinProject,
  collapsed, toggleCollapsed, archived, openArchived, refreshGroups,
  createProject, createSession, switchProject, switchSession,
  archiveSession, restoreSession, openSettings, closeNavigation, runningSessionIds,
  railMode, onToggleSidebar,
}: SessionSidebarProps) {
  const projectSelector = useProjectSelector();
  const t = useT();
  const searchInputRef = useRef<HTMLInputElement>(null);
  const pendingSearchFocus = useRef(false);

  // Rail search = expand + land in the inline search box once it remounts.
  useEffect(() => {
    if (!railMode && pendingSearchFocus.current) {
      pendingSearchFocus.current = false;
      searchInputRef.current?.focus({ preventScroll: true });
    }
  }, [railMode]);

  return (
    <aside className={`sidebar ${railMode ? "sidebar-rail" : ""}`} aria-label={t("sidebar.aria")}>
      <SidebarLogoRow
        collapsed={railMode}
        onToggleSidebar={onToggleSidebar}
        onCloseNavigation={closeNavigation}
      />

      <NewSessionButton disabled={!selectedProject} collapsed={railMode} onClick={createSession} />

      {/* Roles / Graphs below New Session (dsh nav-item style) */}
      <nav style={{ flexShrink: 0, padding: "0 0 8px", display: "flex", flexDirection: "column", gap: 2 }} aria-label="Workspace">
        <NavLink href="/roles" label={t("sidebar.roles")} icon={<Bot size={15} />} />
        <NavLink href="/graphs" label={t("sidebar.graphs")} icon={<Workflow size={15} />} />
      </nav>

      {/* Rail search: expand then focus the inline header search. */}
      {railMode && (
        <button type="button" className="sidebar-rail-icon" aria-label={t("sidebar.searchSessions")} onClick={() => { pendingSearchFocus.current = true; onToggleSidebar(); }}>
          <Search size={18} aria-hidden="true" />
        </button>
      )}

      {/* Project selector (pin kept) */}
      {!railMode && (
        <ProjectSelector
          projects={projects}
          selectedProject={selectedProject}
          pinnedProjectIds={pinnedProjectIds}
          togglePinProject={togglePinProject}
          onSelect={switchProject}
          onCreate={createProject}
          control={projectSelector}
        />
      )}

      {/* Sessions (pin/archive/tree unchanged; search now inline in the header) */}
      {!railMode && selectedProject && (
        <section className="sidebar-section sidebar-sessions" aria-labelledby="sessions-heading" style={{ flex: "1 1 auto", minHeight: 0, display: "flex", flexDirection: "column" }}>
          <div className="sidebar-sessions-head">
            <span id="sessions-heading" className="sessions-label">{t("sidebar.sessions")}</span>
            <div className="sessions-search">
              <Search size={13} aria-hidden="true" />
              <input
                ref={searchInputRef}
                type="search"
                value={query}
                placeholder={t("sidebar.search")}
                aria-label={t("sidebar.searchSessions")}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={(event) => { if (event.key === "Escape") { setQuery(""); event.currentTarget.blur(); } }}
              />
              {query && (
                <button type="button" aria-label={t("sidebar.clearSearch")} onClick={() => setQuery("")}>
                  <X size={13} aria-hidden="true" />
                </button>
              )}
            </div>
            <button type="button" onClick={refreshGroups} title={t("sidebar.refresh")} aria-label={t("sidebar.refresh")} className="sidebar-icon-btn">
              <RefreshCw size={12} aria-hidden="true" />
            </button>
          </div>
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

      {!railMode && !selectedProject && (
        <div style={{ flex: "1 1 auto", display: "flex", alignItems: "center", justifyContent: "center", padding: 18 }}>
          <div style={{ textAlign: "center", color: "var(--text-dim)", fontSize: 12 }}>
            <div>{t("sidebar.chooseProject")}</div>
            <button onClick={createProject} style={{ marginTop: 12, padding: "6px 14px", border: "1px solid var(--accent)", borderRadius: 6, background: "transparent", color: "var(--accent)", cursor: "pointer", fontSize: 12 }}>
              <Plus size={12} style={{ display: "inline", verticalAlign: "middle", marginRight: 4 }} />
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
          {!railMode && <span>{t("sidebar.settings")}</span>}
        </button>
      </div>
    </aside>
  );
}
