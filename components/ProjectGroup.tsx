"use client";

import { Archive, ChevronRight, Star } from "lucide-react";
import { useState } from "react";
import type { Session } from "@/components/settings/types";
import type { SidebarProjectGroup } from "@/hooks/useSidebarProjectGroups";
import { buildSessionTree, renderSessionTree } from "./SessionTree";

export function ProjectGroup({
  group, isCurrent, isPinned, isCollapsed, onToggleCollapsed, onTogglePin,
  query, selectedSession, switchSession, archiveSession, restoreSession, mutatingId,
  archived, onOpenArchived, runningSessionIds,
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
  onOpenArchived: () => void;
  runningSessionIds?: Set<string>;
}) {
  const [archivedOpen, setArchivedOpen] = useState(false);
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
          <button type="button" className="sidebar-archived-toggle" aria-expanded={archivedOpen} onClick={() => {
            const next = !archivedOpen;
            setArchivedOpen(next);
            if (next) onOpenArchived();
          }}>
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
