"use client";

import { Archive, Bot, ChevronRight, MoreHorizontal, Plus, RefreshCw, RotateCcw, Search, Settings2, Star, Workflow, X } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import type { Session } from "@/components/settings/types";
import type { SessionLifecycleView } from "@/hooks/useProjectSessions";
import type { SidebarProjectGroup } from "@/hooks/useSidebarProjectGroups";

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

/** Build a simple session tree from sourceSessionId parent references */
interface SessionTreeNode {
  session: Session;
  children: SessionTreeNode[];
}

function buildSessionTree(sessions: Session[]): SessionTreeNode[] {
  const byId = new Map<string, SessionTreeNode>(
    sessions.map((session) => [session.id, { session, children: [] as SessionTreeNode[] }]),
  );
  const roots: SessionTreeNode[] = [];

  for (const node of byId.values()) {
    const parentId = node.session.sourceSessionId;
    if (!parentId || !byId.has(parentId) || wouldCreateCycle(node.session.id, parentId, byId)) {
      roots.push(node);
      continue;
    }
    byId.get(parentId)!.children.push(node);
  }

  const sortNodes = (nodes: SessionTreeNode[]) => {
    nodes.sort((left, right) => right.session.updatedAt.localeCompare(left.session.updatedAt));
    nodes.forEach((node) => sortNodes(node.children));
  };
  sortNodes(roots);
  return roots;
}

function wouldCreateCycle(sessionId: string, parentId: string, nodes: Map<string, SessionTreeNode>): boolean {
  const visited = new Set<string>([sessionId]);
  let current: string | undefined = parentId;
  while (current) {
    if (visited.has(current)) return true;
    visited.add(current);
    current = nodes.get(current)?.session.sourceSessionId;
  }
  return false;
}

function renderSessionTree(
  nodes: SessionTreeNode[],
  selectedSession: string | null,
  switchSession: (id: string) => void,
  view: SessionLifecycleView,
  archiveSession: (s: Session) => void,
  restoreSession: (s: Session) => void,
  mutatingId: string | null,
  depth: number,
  runningSessionIds?: Set<string>,
): React.ReactNode[] {
  return nodes.flatMap((node) => {
    const s = node.session;
    const isSelected = s.id === selectedSession;
    const isRunning = runningSessionIds?.has(s.id);
    const SESSION_TRUNCATE_LENGTH = 80;

    return [
      <li className="session-row" key={s.id} style={{ paddingLeft: depth * 14 }}>
        <button
          type="button"
          className={`sidebar-item ${isSelected ? "active" : ""}`}
          aria-current={isSelected ? "page" : undefined}
          onClick={() => view === "active" && switchSession(s.id)}
          disabled={view === "archived"}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 6,
            minWidth: 0,
            paddingRight: 4,
          }}
          title={s.title}
        >
          {isRunning && (
            <span
              style={{
                display: "inline-block",
                width: 7,
                height: 7,
                borderRadius: "50%",
                background: "#22c55e",
                flexShrink: 0,
                animation: depth === 0 ? "pulse 2s infinite" : undefined,
              }}
            />
          )}
          <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {s.title.length > SESSION_TRUNCATE_LENGTH
              ? s.title.slice(0, SESSION_TRUNCATE_LENGTH) + "..."
              : s.title}
          </span>
        </button>
        <SessionActionMenu
          session={s}
          view={view}
          archiveSession={archiveSession}
          restoreSession={restoreSession}
          mutatingId={mutatingId}
        />
      </li>,
      ...renderSessionTree(node.children, selectedSession, switchSession, view, archiveSession, restoreSession, mutatingId, depth + 1, runningSessionIds),
    ];
  });
}

function SessionActionMenu({
  session,
  view,
  archiveSession,
  restoreSession,
  mutatingId,
}: {
  session: Session;
  view: SessionLifecycleView;
  archiveSession: (s: Session) => void;
  restoreSession: (s: Session) => void;
  mutatingId: string | null;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [open]);

  return (
    <div className="session-actions" ref={rootRef}>
      <button
        type="button"
        className="session-action-trigger"
        aria-label={`Actions for ${session.title}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((c) => !c)}
      >
        <MoreHorizontal size={15} aria-hidden="true" />
      </button>
      {open && (
        <div className="session-action-menu" role="menu">
          <button
            type="button"
            role="menuitem"
            disabled={mutatingId === session.id}
            onClick={() => {
              setOpen(false);
              if (view === "active") archiveSession(session);
              else restoreSession(session);
            }}
          >
            {view === "active" ? <Archive size={14} aria-hidden="true" /> : <RotateCcw size={14} aria-hidden="true" />}
            {view === "active" ? "Archive session" : "Restore session"}
          </button>
        </div>
      )}
    </div>
  );
}

export function SessionSidebar({
  projects, groups, selectedProject, selectedSession, settingsOpen,
  query, setQuery, mutatingId, announcement, pinnedProjectIds, togglePinProject,
  collapsed, toggleCollapsed, archived, openArchived, refreshGroups,
  createProject, createSession, switchProject, switchSession,
  archiveSession, restoreSession, openSettings, closeNavigation, runningSessionIds,
}: SessionSidebarProps) {
  const [projectDropdownOpen, setProjectDropdownOpen] = useState(false);
  const projectDropdownRef = useRef<HTMLDivElement>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [archivedOpen, setArchivedOpen] = useState<Record<string, boolean>>({});

  useEffect(() => {
    if (!projectDropdownOpen) return;
    const close = (e: PointerEvent) => {
      if (!projectDropdownRef.current?.contains(e.target as Node)) setProjectDropdownOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [projectDropdownOpen]);

  return (
    <aside className="sidebar" aria-label="Projects and sessions">
      {/* Brand header */}
      <div
        style={{
          padding: "12px 10px 10px",
          borderBottom: "1px solid var(--border)",
          flexShrink: 0,
        }}
      >
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
            <span className="brand-mark">E</span>
            <strong style={{ fontSize: 13 }}>Ennote</strong>
          </div>
          <div style={{ display: "flex", gap: 6 }}>
            <button
              onClick={createSession}
              disabled={!selectedProject}
              title={selectedProject ? "New chat" : "Select a project first"}
              style={{
                display: "flex", alignItems: "center", justifyContent: "center", gap: 5,
                background: "var(--bg-hover)",
                border: "1px solid var(--border)",
                color: "var(--text-muted)", cursor: selectedProject ? "pointer" : "not-allowed",
                height: 32, paddingLeft: 10, paddingRight: 12,
                borderRadius: 7, fontSize: 12, fontWeight: 500, opacity: selectedProject ? 1 : 0.5,
                flexShrink: 0, transition: "background 0.12s, color 0.12s, border-color 0.12s",
              }}
              onMouseEnter={(e) => {
                if (!selectedProject) return;
                e.currentTarget.style.background = "var(--bg-selected)";
                e.currentTarget.style.color = "var(--accent)";
                e.currentTarget.style.borderColor = "rgba(37,99,235,0.35)";
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.background = "var(--bg-hover)";
                e.currentTarget.style.color = "var(--text-muted)";
                e.currentTarget.style.borderColor = "var(--border)";
              }}
            >
              <Plus size={12} />
              New Chat
            </button>
            <button
              type="button"
              onClick={() => setSearchOpen((o) => !o)}
              disabled={!selectedProject}
              aria-label="Search sessions"
              aria-expanded={searchOpen}
              title={selectedProject ? "Search sessions" : "Select a project first"}
              style={{
                display: "flex", alignItems: "center", justifyContent: "center",
                background: searchOpen ? "var(--bg-selected)" : "var(--bg-hover)",
                border: "1px solid var(--border)",
                color: searchOpen ? "var(--accent)" : "var(--text-muted)",
                cursor: selectedProject ? "pointer" : "not-allowed",
                width: 32, height: 32, opacity: selectedProject ? 1 : 0.5,
                borderRadius: 7, padding: 0, flexShrink: 0,
                transition: "background 0.12s, color 0.12s, border-color 0.12s",
              }}
              onMouseEnter={(e) => {
                if (!selectedProject) return;
                e.currentTarget.style.background = "var(--bg-selected)";
                e.currentTarget.style.color = "var(--accent)";
                e.currentTarget.style.borderColor = "rgba(37,99,235,0.35)";
              }}
              onMouseLeave={(e) => {
                if (!selectedProject || searchOpen) return;
                e.currentTarget.style.background = "var(--bg-hover)";
                e.currentTarget.style.color = "var(--text-muted)";
                e.currentTarget.style.borderColor = "var(--border)";
              }}
            >
              <Search size={15} aria-hidden="true" />
            </button>
            <button
              onClick={closeNavigation}
              className="icon-btn navigation-close"
              aria-label="Close navigation"
              title="Close navigation"
            >
              <X size={15} />
            </button>
          </div>
        </div>
      </div>

      {/* Session search: expands from the header search button (like annovibe) */}
      {searchOpen && (
        <div style={{ flexShrink: 0, padding: "8px 10px 0" }}>
          <label className="session-search" style={{ margin: 0 }}>
            <Search size={14} aria-hidden="true" />
            <span className="sr-only">Search sessions</span>
            <input
              type="search"
              autoFocus
              value={query}
              placeholder="Search sessions…"
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => { if (event.key === "Escape") setSearchOpen(false); }}
            />
            <button
              type="button"
              onClick={() => { setQuery(""); setSearchOpen(false); }}
              aria-label="Clear search"
              style={{ display: "grid", placeItems: "center", width: 18, height: 18, padding: 0, border: 0, background: "transparent", color: "var(--text-dim)", cursor: "pointer" }}
            >
              <X size={13} />
            </button>
          </label>
        </div>
      )}

      {/* Primary navigation: Roles / Graphs (independent routes) */}

      <nav style={{ flexShrink: 0, padding: "8px 10px 2px", display: "flex", flexDirection: "column", gap: 2 }} aria-label="Workspace">
        <NavLink href="/roles" label="Roles" icon={<Bot size={15} />} />
        <NavLink href="/graphs" label="Graphs" icon={<Workflow size={15} />} />
      </nav>

      {/* Project selector */}
      <div ref={projectDropdownRef} style={{ position: "relative", marginTop: 8, padding: "0 10px", display: "flex", alignItems: "stretch", gap: 4 }}>
        <button
          type="button"
          onClick={() => setProjectDropdownOpen((o) => !o)}
          style={{
            flex: 1, minWidth: 0,
            display: "flex", alignItems: "center", gap: 6,
            padding: "6px 10px",
            background: selectedProject ? "var(--bg-hover)" : "rgba(37,99,235,0.06)",
            border: selectedProject ? "1px solid var(--border)" : "1px solid rgba(37,99,235,0.4)",
            borderRadius: 7, cursor: "pointer",
            fontSize: 12, color: "var(--text)", textAlign: "left" as const,
          }}
          title={selectedProject ? projects.find((p) => p.id === selectedProject)?.name ?? "Select project" : "Select project"}
          aria-haspopup="menu"
          aria-expanded={projectDropdownOpen}
        >
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, color: "var(--accent)" }}>
            <path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5Z" />
          </svg>
          <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontFamily: "var(--font-mono)", fontSize: 11, color: selectedProject ? "var(--text)" : "var(--text-dim)" }}>
            {selectedProject ? (projects.find((p) => p.id === selectedProject)?.name ?? "Unknown") : "Select project..."}
          </span>
          <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="var(--text-dim)" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, transform: projectDropdownOpen ? "rotate(180deg)" : "none", transition: "transform 0.15s" }}>
            <polyline points="2 3.5 5 6.5 8 3.5" />
          </svg>
        </button>

        {projectDropdownOpen && (
          <div
            role="menu"
            aria-label="Projects"
            style={{
              position: "absolute", top: "calc(100% + 4px)", left: 10, right: 10, zIndex: 100,
              background: "var(--bg)", border: "1px solid var(--border)", borderRadius: 8,
              boxShadow: "0 6px 20px rgba(0,0,0,0.10)", overflow: "hidden", maxHeight: 280, overflowY: "auto",
            }}
          >
            {projects.length === 0 && (
              <div style={{ padding: "10px 12px", color: "var(--text-dim)", fontSize: 11 }}>No projects yet</div>
            )}
            {projects.map((project) => (
              <div key={project.id} style={{ display: "flex", alignItems: "center" }}>
                <button
                  type="button"
                  onClick={() => {
                    switchProject(project.id);
                    setProjectDropdownOpen(false);
                  }}
                  style={{
                    display: "flex", alignItems: "center", gap: 8,
                    flex: 1, minWidth: 0, padding: "8px 4px 8px 10px",
                    background: project.id === selectedProject ? "var(--bg-selected)" : "transparent",
                    border: "none", color: "var(--text)", cursor: "pointer",
                    textAlign: "left" as const, fontSize: 12, fontWeight: project.id === selectedProject ? 600 : 400,
                    transition: "background 0.1s",
                  }}
                  onMouseEnter={(e) => { if (project.id !== selectedProject) e.currentTarget.style.background = "var(--bg-hover)"; }}
                  onMouseLeave={(e) => { if (project.id !== selectedProject) e.currentTarget.style.background = "transparent"; }}
                >
                  <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, color: "var(--accent)", opacity: project.id === selectedProject ? 1 : 0.6 }}>
                    <path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5Z" />
                  </svg>
                  <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{project.name}</span>
                </button>
                <button
                  type="button"
                  title={`${pinnedProjectIds.includes(project.id) ? "Unpin" : "Pin"} project ${project.name}`}
                  aria-label={pinnedProjectIds.includes(project.id) ? "Unpin project" : "Pin project"}
                  onClick={() => togglePinProject(project.id)}
                  style={{ display: "grid", placeItems: "center", width: 26, height: 30, marginRight: 2, padding: 0, border: "none", borderRadius: 5, background: "transparent", cursor: "pointer", flexShrink: 0, color: pinnedProjectIds.includes(project.id) ? "#ca8a04" : "var(--text-dim)" }}
                >
                  <Star size={13} fill={pinnedProjectIds.includes(project.id) ? "currentColor" : "none"} aria-hidden="true" />
                </button>
              </div>
            ))}
            <button
              type="button"
              onClick={() => {
                setProjectDropdownOpen(false);
                createProject();
              }}
              style={{
                display: "flex", alignItems: "center", gap: 7,
                width: "100%", padding: "8px 10px",
                background: "none", border: "none",
                borderTop: projects.length > 0 ? "1px solid var(--border)" : "none",
                color: "var(--accent)", cursor: "pointer", textAlign: "left" as const, fontSize: 11,
              }}
            >
              <Plus size={12} />
              Add project...
            </button>
          </div>
        )}
      </div>

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
                  archivedOpen={Boolean(archivedOpen[group.projectId])}
                  onToggleArchived={() => {
                    const next = !archivedOpen[group.projectId];
                    setArchivedOpen((cur) => ({ ...cur, [group.projectId]: next }));
                    if (next) openArchived(group.projectId);
                  }}
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

function ProjectGroup({
  group, isCurrent, isPinned, isCollapsed, onToggleCollapsed, onTogglePin,
  query, selectedSession, switchSession, archiveSession, restoreSession, mutatingId,
  archived, archivedOpen, onToggleArchived, runningSessionIds,
}: {
  group: SidebarProjectGroup;
  isCurrent: boolean;
  isPinned: boolean;
  isCollapsed: boolean;
  onToggleCollapsed: () => void;
  onTogglePin: () => void;
  query: string;
  selectedSession: string | null;
  switchSession: (id: string) => void;
  archiveSession: (s: Session) => void;
  restoreSession: (s: Session) => void;
  mutatingId: string | null;
  archived: Session[];
  archivedOpen: boolean;
  onToggleArchived: () => void;
  runningSessionIds?: Set<string>;
}) {
  const matching = query
    ? group.sessions.filter((s) => s.title.toLowerCase().includes(query))
    : group.sessions;
  return (
    <div className="sidebar-project-group">
      <ProjectGroupHeader
        group={group}
        isCurrent={isCurrent}
        isPinned={isPinned}
        isCollapsed={isCollapsed}
        onToggleCollapsed={onToggleCollapsed}
        onTogglePin={onTogglePin}
      />
      {!isCollapsed && (
        <div>
          {group.error && <div className="sidebar-empty" style={{ color: "var(--danger)" }}>{group.error}</div>}
          {group.loading && matching.length === 0 ? (
            <div className="sidebar-empty">Loading…</div>
          ) : matching.length === 0 ? (
            <div className="sidebar-empty" role="status">
              {query ? `No sessions match "${query}"` : "No sessions yet."}
            </div>
          ) : (
            <ul className="session-list" aria-label={`Sessions in ${group.projectName}`} style={{ padding: "0 0 2px" }}>
              {renderSessionTree(buildSessionTree(matching), selectedSession, switchSession, "active", archiveSession, restoreSession, mutatingId, 0, runningSessionIds)}
            </ul>
          )}
          <button type="button" className="sidebar-archived-toggle" aria-expanded={archivedOpen} onClick={onToggleArchived}>
            <Archive size={12} aria-hidden="true" />
            {archivedOpen ? "Hide archived sessions" : "Show archived sessions"}
          </button>
          {archivedOpen && archived.length > 0 && (
            <ul className="session-list" aria-label={`Archived sessions in ${group.projectName}`} style={{ padding: "0 0 6px" }}>
              {renderSessionTree(buildSessionTree(archived), selectedSession, switchSession, "archived", archiveSession, restoreSession, mutatingId, 0, runningSessionIds)}
            </ul>
          )}
          {archivedOpen && archived.length === 0 && (
            <div className="sidebar-empty">No archived sessions.</div>
          )}
        </div>
      )}
    </div>
  );
}

function ProjectGroupHeader({
  group, isCurrent, isPinned, isCollapsed, onToggleCollapsed, onTogglePin,
}: {
  group: SidebarProjectGroup;
  isCurrent: boolean;
  isPinned: boolean;
  isCollapsed: boolean;
  onToggleCollapsed: () => void;
  onTogglePin: () => void;
}) {
  const count = group.sessions.length;
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onToggleCollapsed}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onToggleCollapsed(); } }}
      title={group.projectName}
      style={{
        display: "flex", alignItems: "center", gap: 6, height: 30, padding: "0 8px", marginTop: 2,
        background: isCurrent ? "var(--bg-selected)" : "transparent",
        borderRadius: 6, cursor: "pointer", color: "var(--text)",
        transition: "background 0.12s",
      }}
    >
      <ChevronRight size={13} aria-hidden="true" style={{ flexShrink: 0, color: "var(--text-dim)", transform: isCollapsed ? "none" : "rotate(90deg)", transition: "transform 0.15s" }} />
      <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, color: isCurrent ? "var(--accent)" : "var(--text-dim)" }}>
        <path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5Z" />
      </svg>
      <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontSize: 12, fontWeight: isCurrent ? 650 : 550 }}>
        {group.projectName}
      </span>
      {count > 0 && <span style={{ flexShrink: 0, color: "var(--text-dim)", fontSize: 10 }}>{count} {count === 1 ? "chat" : "chats"}</span>}
      <button
        type="button"
        title={`${isPinned ? "Unpin" : "Pin"} project ${group.projectName}`}
        aria-label={isPinned ? "Unpin project" : "Pin project"}
        onClick={(e) => { e.stopPropagation(); onTogglePin(); }}
        style={{ display: "grid", placeItems: "center", width: 22, height: 22, padding: 0, border: "none", borderRadius: 5, background: "transparent", cursor: "pointer", flexShrink: 0, color: isPinned ? "#ca8a04" : "var(--text-dim)", opacity: isPinned ? 1 : 0.45 }}
      >
        <Star size={13} fill={isPinned ? "currentColor" : "none"} aria-hidden="true" />
      </button>
    </div>
  );
}

function NavLink({ href, label, icon }: { href: string; label: string; icon: React.ReactNode }) {
  const pathname = usePathname();
  const active = pathname === href;
  return (
    <Link
      href={href}
      className={`sidebar-item ${active ? "active" : ""}`}
      style={{
        display: "flex", alignItems: "center", gap: 8,
        padding: "6px 10px", borderRadius: 7,
        color: active ? "var(--text)" : "var(--text-muted)",
        fontSize: 12, fontWeight: active ? 600 : 400,
        textDecoration: "none",
      }}
    >
      <span style={{ color: active ? "var(--accent)" : "inherit", display: "flex", flexShrink: 0 }}>{icon}</span>
      {label}
    </Link>
  );
}
