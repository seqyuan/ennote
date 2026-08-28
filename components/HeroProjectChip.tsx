"use client";

import { useT } from "@/components/LocaleProvider";
import type { useProjectSelector } from "@/hooks/useProjectSelector";
import { ProjectMenu, type SidebarProject } from "./ProjectMenu";

/**
 * Workspace switch chip for the chat hero, mirroring the DeepSeek Harness
 * conversation hero: the chip rides directly above the composer card, at its
 * top-left — never the floating top-left corner of the chat area. It carries
 * the only project affordance of the empty state: picking opens the shared
 * project menu with the add entry pinned at the bottom; with no projects at
 * all the chip opens the create flow directly.
 */
export function HeroProjectChip(props: {
  projects: SidebarProject[];
  selectedProject: string | null;
  pinnedProjectIds: string[];
  togglePinProject: (projectId: string) => void;
  /** Open the project menu (or the create dialog when no project exists). */
  onRequestProject: () => void;
  onSwitchProject: (projectId: string) => void;
  /** Dropdown control owned by ChatWindow (shared with the composer). */
  projectSelector: ReturnType<typeof useProjectSelector>;
}) {
  const { projects, selectedProject, pinnedProjectIds, togglePinProject, onRequestProject, onSwitchProject, projectSelector } = props;
  const { open, close, rootRef } = projectSelector;
  const t = useT();
  const selectedName = selectedProject ? projects.find((p) => p.id === selectedProject)?.name ?? null : null;

  return (
    <div ref={rootRef} className="hero-project-chip-wrap">
      <button
        type="button"
        className="hero-project-chip"
        onClick={onRequestProject}
        title={selectedName ?? t("empty.project.choose")}
        aria-label={selectedName ? `${selectedName} · ${t("sidebar.selectProject")}` : t("sidebar.selectProjectEllipsis")}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        {selectedName ? (
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5ZM1.5 8V11A1.5 1.5 0 0 0 3 12.5h10A1.5 1.5 0 0 0 14.5 11V8" />
          </svg>
        ) : (
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5Z" />
          </svg>
        )}
        <span className="hero-chip-label">
          {selectedName ?? t("empty.project.choose")}
        </span>
        <svg className="hero-chip-chevron" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M3 4.5L6 7.5L9 4.5" />
        </svg>
      </button>

      {open && (
        <ProjectMenu
          projects={projects}
          selectedProject={selectedProject}
          pinnedProjectIds={pinnedProjectIds}
          togglePinProject={togglePinProject}
          onSelect={(projectId) => {
            close();
            onSwitchProject(projectId);
          }}
          className="hero-project-menu"
        />
      )}
    </div>
  );
}
