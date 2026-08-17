"use client";

import { Star } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

export interface SidebarProject { id: string; name: string }

/**
 * Shared project dropdown menu (list + pin), used by the sidebar
 * ProjectSelector and the chat hero's project chip. The caller owns the
 * anchored wrapper, the open state, and the outside-click lifecycle; this
 * component only renders the menu panel. There is deliberately NO add-entry
 * here — workspace creation lives in the sidebar's own add button and the
 * hero chip's no-project create flow. `project-menu` is the base class that
 * supplies the popover chrome (absolute positioning, surface, border,
 * shadow, max-height); the caller's positioning class sets the anchor.
 */
export function ProjectMenu(props: {
  projects: SidebarProject[];
  selectedProject: string | null;
  pinnedProjectIds: string[];
  togglePinProject: (projectId: string) => void;
  onSelect: (projectId: string) => void;
  /** Positioning class (e.g. `sidebar-project-menu` / `hero-project-menu`). */
  className?: string;
}) {
  const { projects, selectedProject, pinnedProjectIds, togglePinProject, onSelect, className } = props;
  const t = useT();

  return (
    <div role="menu" aria-label="Projects" className={`project-menu ${className ?? ""}`}>
      {projects.length === 0 && (
        <div style={{ padding: "10px 12px", color: "var(--text-dim)", fontSize: 11 }}>{t("sidebar.noProjects")}</div>
      )}
      {projects.map((project) => (
        <div key={project.id} style={{ display: "flex", alignItems: "center" }}>
          <button
            type="button"
            onClick={() => onSelect(project.id)}
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
            title={`${pinnedProjectIds.includes(project.id) ? t("sidebar.unpin") : t("sidebar.pin")} ${project.name}`}
            aria-label={pinnedProjectIds.includes(project.id) ? t("sidebar.unpin") : t("sidebar.pin")}
            onClick={() => togglePinProject(project.id)}
            style={{ display: "grid", placeItems: "center", width: 26, height: 30, marginRight: 2, padding: 0, border: "none", borderRadius: 5, background: "transparent", cursor: "pointer", flexShrink: 0, color: pinnedProjectIds.includes(project.id) ? "#ca8a04" : "var(--text-dim)" }}
          >
            <Star size={13} fill={pinnedProjectIds.includes(project.id) ? "currentColor" : "none"} aria-hidden="true" />
          </button>
        </div>
      ))}
    </div>
  );
}
