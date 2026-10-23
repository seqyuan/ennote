"use client";

import { ChevronDown, Folder } from "lucide-react";
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
          background: selectedProject ? "var(--bg-hover)" : "var(--accent-soft)",
          border: selectedProject ? "1px solid var(--border)" : "1px solid var(--accent)",
          borderRadius: "var(--radius-sm)", cursor: "pointer",
          fontSize: "var(--text-sm)", color: "var(--text)", textAlign: "left" as const,
        }}
        title={selectedProject ? projects.find((p) => p.id === selectedProject)?.name ?? t("sidebar.selectProject") : t("sidebar.selectProject")}
        aria-haspopup="menu"
        aria-expanded={projectDropdownOpen}
      >
        <Folder size={14} aria-hidden="true" style={{ flexShrink: 0, color: "var(--accent)" }} />
        <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontFamily: "var(--font-mono)", fontSize: "var(--text-xs)", color: selectedProject ? "var(--text)" : "var(--text-dim)" }}>
          {selectedProject ? (projects.find((p) => p.id === selectedProject)?.name ?? t("sidebar.unknown")) : t("sidebar.selectProjectEllipsis")}
        </span>
        <ChevronDown size={12} aria-hidden="true" style={{ flexShrink: 0, transform: projectDropdownOpen ? "rotate(180deg)" : "none", transition: "transform 0.15s" }} />
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
