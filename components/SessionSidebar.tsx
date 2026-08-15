"use client";

import { Bot, Plus, RefreshCw, Settings2, Workflow } from "lucide-react";
import { useState } from "react";
import type { Session } from "@/components/settings/types";
import type { SidebarProjectGroup } from "@/hooks/useSidebarProjectGroups";
import { useProjectSelector } from "@/hooks/useProjectSelector";
import { BrandHeader } from "./BrandHeader";
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
}

export function SessionSidebar({
  projects, groups, selectedProject, selectedSession, settingsOpen,
  query, setQuery, mutatingId, announcement, pinnedProjectIds, togglePinProject,
  collapsed, toggleCollapsed, archived, openArchived, refreshGroups,
  createProject, createSession, switchProject, switchSession,
  archiveSession, restoreSession, openSettings, closeNavigation, runningSessionIds,
}: SessionSidebarProps) {
  const [searchOpen, setSearchOpen] = useState(false);
  const projectSelector = useProjectSelector();

  return (
    <aside className="sidebar" aria-label="Projects and sessions">
      <BrandHeader
        createDisabled={!selectedProject}
        onNewChat={createSession}
        searchOpen={searchOpen}
        onToggleSearch={() => setSearchOpen((o) => !o)}
        onCloseNavigation={closeNavigation}
      />

      {/* Session search: expands from the header search button (like annovibe) */}
      {searchOpen && (
        <SessionSearch
          value={query}
          onChange={setQuery}
          onClear={() => { setQuery(""); setSearchOpen(false); }}
          onEscape={() => setSearchOpen(false)}
        />
      )}

      {/* Primary navigation: Roles / Graphs (independent routes) */}

      <nav style={{ flexShrink: 0, padding: "8px 10px 2px", display: "flex", flexDirection: "column", gap: 2 }} aria-label="Workspace">
        <NavLink href="/roles" label="Roles" icon={<Bot size={15} />} />
        <NavLink href="/graphs" label="Graphs" icon={<Workflow size={15} />} />
      </nav>

      {/* Project selector */}
      <ProjectSelector
        projects={projects}
        selectedProject={selectedProject}
        pinnedProjectIds={pinnedProjectIds}
        togglePinProject={togglePinProject}
        onSelect={switchProject}
        onCreate={createProject}
        control={projectSelector}
      />

      {/* Pinned/current project groups with flat session lists (annovibe style) */}
      {selectedProject && (
        <section className="sidebar-section sidebar-sessions" aria-labelledby="sessions-heading" style={{ flex: "1 1 auto", minHeight: 0, display: "flex", flexDirection: "column" }}>
          <div style={{ flexShrink: 0, padding: "8px 10px 4px" }}>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
              <span id="sessions-heading" style={{ color: "var(--text-dim)", fontSize: 10, fontWeight: 600, letterSpacing: "0.05em", textTransform: "uppercase" }}>
                Sessions
              </span>
              <button
                type="button"
                onClick={refreshGroups}
                title="Refresh sessions"
                aria-label="Refresh sessions"
                className="sidebar-icon-btn"
              >
                <RefreshCw size={12} aria-hidden="true" />
              </button>
            </div>
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

      {!selectedProject && (
        <div style={{ flex: "1 1 auto", display: "flex", alignItems: "center", justifyContent: "center", padding: 18 }}>
          <div style={{ textAlign: "center", color: "var(--text-dim)", fontSize: 12 }}>
            <div>Choose a project to start.</div>
            <button
              onClick={createProject}
              style={{
                marginTop: 12, padding: "6px 14px", border: "1px solid var(--accent)", borderRadius: 6,
                background: "transparent", color: "var(--accent)", cursor: "pointer", fontSize: 12,
              }}
            >
              <Plus size={12} style={{ display: "inline", verticalAlign: "middle", marginRight: 4 }} />
              New Project
            </button>
          </div>
        </div>
      )}

      <span className="sr-only" role="status" aria-live="polite">{announcement}</span>

      {/* Settings at bottom */}
      <div className="sidebar-section sidebar-settings" style={{ flexShrink: 0 }}>
        <button
          className={`sidebar-item sidebar-button ${settingsOpen ? "active" : ""}`}
          onClick={openSettings}
          aria-haspopup="dialog"
          aria-expanded={settingsOpen}
        >
          <Settings2 size={15} aria-hidden="true" />
          <span>Settings</span>
        </button>
      </div>
    </aside>
  );
}
