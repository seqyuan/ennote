"use client";

import { Folder, Star } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import styles from "./ProjectMenu.module.css";

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
        <div className={styles.empty}>{t("sidebar.noProjects")}</div>
      )}
      {projects.map((project) => (
        <div key={project.id} className={styles.row}>
          <button
            type="button"
            onClick={() => onSelect(project.id)}
            className={styles.select}
            data-selected={project.id === selectedProject || undefined}
          >
            <Folder size={14} aria-hidden="true" className={styles.folder} />
            <span className={styles.name}>{project.name}</span>
          </button>
          <button
            type="button"
            title={`${pinnedProjectIds.includes(project.id) ? t("sidebar.unpin") : t("sidebar.pin")} ${project.name}`}
            aria-label={pinnedProjectIds.includes(project.id) ? t("sidebar.unpin") : t("sidebar.pin")}
            onClick={() => togglePinProject(project.id)}
            className={styles.pin}
            data-pinned={pinnedProjectIds.includes(project.id) || undefined}
          >
            <Star size={13} fill={pinnedProjectIds.includes(project.id) ? "currentColor" : "none"} aria-hidden="true" />
          </button>
        </div>
      ))}
    </div>
  );
}
