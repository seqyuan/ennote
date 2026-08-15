"use client";

import { Bot, Plus, RefreshCw, Search, Settings2, Workflow } from "lucide-react";
import { useState } from "react";
import type { Session } from "@/components/settings/types";
import type { SidebarProjectGroup } from "@/hooks/useSidebarProjectGroups";
import { useProjectSelector } from "@/hooks/useProjectSelector";
import { SidebarLogoRow } from "./SidebarLogoRow";
import { NewSessionButton } from "./NewSessionButton";
import { NavLink } from "./NavLink";
import { ProjectGroup } from "./ProjectGroup";
import { ProjectSelector } from "./ProjectSelector";
import { SessionSearch } from "./SessionSearch";

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
  const [searchOpen, setSearchOpen] = useState(false);
  const projectSelector = useProjectSelector();

  return (
    <aside className={`sidebar ${railMode ? "sidebar-rail" : ""}`} aria-label="Projects and sessions">
      <SidebarLogoRow
        collapsed={railMode}
        onToggleSidebar={onToggleSidebar}
        onCloseNavigation={closeNavigation}
      />

      <NewSessionButton disabled={!selectedProject} collapsed={railMode} onClick={createSession} />

      {/* Roles / Graphs below New Session (dsh nav-item style) */}
      <nav style={{ flexShrink: 0, padding: "0 0 8px", display: "flex", flexDirection: "column", gap: 2 }} aria-label="Workspace">
        <NavLink href="/roles" label="Roles" icon={<Bot size={15} />} />
        <NavLink href="/graphs" label="Graphs" icon={<Workflow size={15} />} />
      </nav>

      {/* Search entry: icon in the rail; expandable input in wide mode. */}
      {!railMode && (
        <div style={{ flexShrink: 0, padding: "0 10px 8px" }}>
          {searchOpen ? (
            <SessionSearch
              value={query}
              onChange={setQuery}
              onClear={() => { setQuery(""); setSearchOpen(false); }}
              onEscape={() => setSearchOpen(false)}
            />
          ) : (
            <button type="button" className="sidebar-search-trigger" aria-label="Search sessions" onClick={() => setSearchOpen(true)}>
              <Search size={14} aria-hidden="true" />
              <span>Search sessions</span>
            </button>
          )}
        </div>
      )}
      {railMode && (
        <button type="button" className="sidebar-rail-icon" aria-label="Search sessions" onClick={() => { setSearchOpen(true); onToggleSidebar(); }}>
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

      {/* Sessions (pin/archive/tree unchanged, restyled via CSS) */}
      {!railMode && selectedProject && (
        <section className="sidebar-section sidebar-sessions" aria-labelledby="sessions-heading" style={{ flex: "1 1 auto", minHeight: 0, display: "flex", flexDirection: "column" }}>
          <div className="sidebar-sessions-head">
            <span id="sessions-heading" className="sessions-label">Sessions</span>
            <button type="button" onClick={refreshGroups} title="Refresh sessions" aria-label="Refresh sessions" className="sidebar-icon-btn">
              <RefreshCw size={12} aria-hidden="true" />
            </button>
          </div>
          <div style={{ flex: "1 1 auto", overflowY: "auto", minHeight: 0, padding: "0 6px 8px" }}>
            {groups.length === 0 ? (
              <div className="sidebar-empty">Loading sessions…</div>
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
            <div>Choose a project to start.</div>
            <button onClick={createProject} style={{ marginTop: 12, padding: "6px 14px", border: "1px solid var(--accent)", borderRadius: 6, background: "transparent", color: "var(--accent)", cursor: "pointer", fontSize: 12 }}>
              <Plus size={12} style={{ display: "inline", verticalAlign: "middle", marginRight: 4 }} />
              New Project
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
          {!railMode && <span>Settings</span>}
        </button>
      </div>
    </aside>
  );
}
