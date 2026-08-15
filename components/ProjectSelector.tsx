"use client";

import { Plus, Star } from "lucide-react";
import type { useProjectSelector } from "@/hooks/useProjectSelector";

interface SidebarProject { id: string; name: string }

export function ProjectSelector(props: {
  projects: SidebarProject[];
  selectedProject: string | null;
  pinnedProjectIds: string[];
  togglePinProject: (projectId: string) => void;
  onSelect: (projectId: string) => void;
  onCreate: () => void;
  control: ReturnType<typeof useProjectSelector>;
}) {
  const { projects, selectedProject, pinnedProjectIds, togglePinProject, onSelect, onCreate, control } = props;
  const { open: projectDropdownOpen, toggle, close, rootRef: controlRootRef } = control;

  return (
    <div ref={controlRootRef} style={{ position: "relative", marginTop: 8, padding: "0 10px", display: "flex", alignItems: "stretch", gap: 4 }}>
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
                  onSelect(project.id);
                  close();
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
              close();
              onCreate();
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
  );
}
