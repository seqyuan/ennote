"use client";

import { useT } from "@/components/LocaleProvider";
import type { useProjectSelector } from "@/hooks/useProjectSelector";
import { ProjectMenu, type SidebarProject } from "./ProjectMenu";

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
    <div ref={controlRootRef} className="sidebar-project-selector" style={{ position: "relative", padding: "0 10px", display: "flex", alignItems: "stretch", gap: 4 }}>
      <button
        type="button"
        onClick={toggle}
        style={{
          flex: 1, minWidth: 0,
          display: "flex", alignItems: "center", gap: 6,
          padding: "6px 10px",
          background: selectedProject ? "var(--bg-hover)" : "rgba(37,99,235,0.06)",
          border: selectedProject ? "1px solid var(--border)" : "1px solid rgba(37,99,235,0.4)",
          borderRadius: 7, cursor: "pointer",
          fontSize: 12, color: "var(--text)", textAlign: "left" as const,
        }}
        title={selectedProject ? projects.find((p) => p.id === selectedProject)?.name ?? t("sidebar.selectProject") : t("sidebar.selectProject")}
        aria-haspopup="menu"
        aria-expanded={projectDropdownOpen}
      >
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, color: "var(--accent)" }}>
          <path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5Z" />
        </svg>
        <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontFamily: "var(--font-mono)", fontSize: 11, color: selectedProject ? "var(--text)" : "var(--text-dim)" }}>
          {selectedProject ? (projects.find((p) => p.id === selectedProject)?.name ?? t("sidebar.unknown")) : t("sidebar.selectProjectEllipsis")}
        </span>
        <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="var(--text-dim)" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, transform: projectDropdownOpen ? "rotate(180deg)" : "none", transition: "transform 0.15s" }}>
          <polyline points="2 3.5 5 6.5 8 3.5" />
        </svg>
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
