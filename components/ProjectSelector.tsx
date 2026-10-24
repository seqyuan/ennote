"use client";

import { ChevronDown, Folder } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import type { useProjectSelector } from "@/hooks/useProjectSelector";
import { ProjectMenu, type SidebarProject } from "./ProjectMenu";
import styles from "./ProjectSelector.module.css";

export { type SidebarProject } from "./ProjectMenu";

export function ProjectSelector(props: {
  projects: SidebarProject[];
  selectedProject: string | null;
  pinnedProjectIds: string[];
  togglePinProject: (projectId: string) => void;
  onSelect: (projectId: string) => void;
  control: ReturnType<typeof useProjectSelector>;
}) {
  const { projects, selectedProject, pinnedProjectIds, togglePinProject, onSelect, control } = props;
  const { open: projectDropdownOpen, toggle, close, rootRef: controlRootRef } = control;
  const t = useT();

  return (
    <div ref={controlRootRef} className={`sidebar-project-selector ${styles.root}`}>
      <button
        type="button"
        onClick={toggle}
        className={styles.trigger}
        data-selected={Boolean(selectedProject) || undefined}
        title={selectedProject ? projects.find((p) => p.id === selectedProject)?.name ?? t("sidebar.selectProject") : t("sidebar.selectProject")}
        aria-haspopup="menu"
        aria-expanded={projectDropdownOpen}
      >
        <Folder size={14} aria-hidden="true" className={styles.folder} />
        <span className={styles.label}>
          {selectedProject ? (projects.find((p) => p.id === selectedProject)?.name ?? t("sidebar.unknown")) : t("sidebar.selectProjectEllipsis")}
        </span>
        <ChevronDown size={12} aria-hidden="true" className={styles.chevron} data-open={projectDropdownOpen || undefined} />
      </button>

      {projectDropdownOpen && (
        <ProjectMenu
          projects={projects}
          selectedProject={selectedProject}
          pinnedProjectIds={pinnedProjectIds}
          togglePinProject={togglePinProject}
          onSelect={(projectId) => {
            onSelect(projectId);
            close();
          }}
          className="sidebar-project-menu"
        />
      )}
    </div>
  );
}
