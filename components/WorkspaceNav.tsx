"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Bot, MessageSquare, Plus, Pin, PinOff, Workflow } from "lucide-react";
import { useState } from "react";
import { useWorkspace } from "@/components/WorkspaceProvider";
import type { components } from "@/lib/worker-api.gen";

type Project = components["schemas"]["Project"];

/**
 * WorkspaceNav is the left rail shared by the Roles (/roles) and Graphs
 * (/graphs) pages: brand header, project switcher, pinned projects, and
 * primary navigation (Chat / Roles / Graphs). The chat shell keeps its own
 * richer SessionSidebar (session tree + search).
 */
export function WorkspaceNav() {
  const {
    projects, selectedProject, switchProject,
    pinnedProjectIds, togglePinProject,
    openCreateProject,
  } = useWorkspace();
  const pathname = usePathname();
  const [projectDropdownOpen, setProjectDropdownOpen] = useState(false);

  const pinned = projects.filter((project) => pinnedProjectIds.includes(project.id));
  const unpinned = projects.filter((project) => !pinnedProjectIds.includes(project.id));

  const navItems = [
    { href: "/", label: "Chat", icon: <MessageSquare size={15} />, active: pathname === "/" },
    { href: "/roles", label: "Roles", icon: <Bot size={15} />, active: pathname === "/roles" },
    { href: "/graphs", label: "Graphs", icon: <Workflow size={15} />, active: pathname === "/graphs" },
  ];

  return (
    <aside className="sidebar" aria-label="Workspace navigation" style={{ display: "flex", flexDirection: "column", minHeight: 0 }}>
      {/* Brand header */}
      <div style={{ padding: "12px 10px 10px", borderBottom: "1px solid var(--border)", flexShrink: 0 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <span className="brand-mark">E</span>
          <strong style={{ fontSize: 13 }}>Ennote</strong>
        </div>
      </div>

      {/* Primary navigation */}
      <nav style={{ padding: "8px 10px", flexShrink: 0 }} aria-label="Primary">
        {navItems.map((item) => (
          <Link
            key={item.href}
            href={item.href}
            className={`sidebar-item ${item.active ? "active" : ""}`}
            style={{
              display: "flex", alignItems: "center", gap: 8,
              padding: "6px 10px", borderRadius: 7,
              color: item.active ? "var(--text)" : "var(--text-muted)",
              fontSize: 12, fontWeight: item.active ? 600 : 400,
              textDecoration: "none",
            }}
          >
            <span style={{ color: item.active ? "var(--accent)" : "inherit", display: "flex" }}>{item.icon}</span>
            {item.label}
          </Link>
        ))}
      </nav>

      {/* Project switcher */}
      <div style={{ position: "relative", padding: "0 10px", marginTop: 4, flexShrink: 0 }}>
        <button
          type="button"
          onClick={() => setProjectDropdownOpen((open) => !open)}
          style={{
            display: "flex", alignItems: "center", gap: 6,
            width: "100%", padding: "6px 10px",
            background: "var(--bg-hover)", border: "1px solid var(--border)",
            borderRadius: 7, cursor: "pointer", fontSize: 12, color: "var(--text)",
          }}
          aria-haspopup="menu"
          aria-expanded={projectDropdownOpen}
        >
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, color: "var(--accent)" }}>
            <path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5Z" />
          </svg>
          <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontFamily: "var(--font-mono)", fontSize: 11 }}>
            {selectedProject ? (projects.find((p) => p.id === selectedProject)?.name ?? "Unknown") : "Select project..."}
          </span>
          <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="var(--text-dim)" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" style={{ transform: projectDropdownOpen ? "rotate(180deg)" : "none", transition: "transform 0.15s" }}>
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
              <button
                key={project.id}
                type="button"
                onClick={() => { switchProject(project.id); setProjectDropdownOpen(false); }}
                style={{
                  display: "flex", alignItems: "center", gap: 8,
                  width: "100%", padding: "8px 10px",
                  background: project.id === selectedProject ? "var(--bg-selected)" : "transparent",
                  border: "none", color: "var(--text)", cursor: "pointer",
                  textAlign: "left", fontSize: 12, fontWeight: project.id === selectedProject ? 600 : 400,
                }}
              >
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" style={{ flexShrink: 0, color: "var(--accent)", opacity: project.id === selectedProject ? 1 : 0.6 }}>
                  <path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5Z" />
                </svg>
                {project.name}
              </button>
            ))}
            <button
              type="button"
              onClick={() => { setProjectDropdownOpen(false); openCreateProject(); }}
              style={{
                display: "flex", alignItems: "center", gap: 7,
                width: "100%", padding: "8px 10px",
                background: "none", border: "none",
                borderTop: projects.length > 0 ? "1px solid var(--border)" : "none",
                color: "var(--accent)", cursor: "pointer", textAlign: "left", fontSize: 11,
              }}
            >
              <Plus size={12} />
              Add project...
            </button>
          </div>
        )}
      </div>

      {/* Pinned projects */}
      {(pinned.length > 0 || unpinned.length > 0) && (
        <div style={{ flexShrink: 0, padding: "10px 10px 4px" }}>
          <div style={{ color: "var(--text-dim)", fontSize: 10, fontWeight: 600, letterSpacing: "0.05em", textTransform: "uppercase" }}>
            Pinned projects
          </div>
        </div>
      )}
      <div style={{ flex: "1 1 auto", overflowY: "auto", minHeight: 0, padding: "0 10px 8px" }}>
        {pinned.length === 0 && unpinned.length === 0 && (
          <div style={{ padding: "8px 2px", color: "var(--text-dim)", fontSize: 11 }}>No projects yet. Create one to get started.</div>
        )}
        {pinned.map((project) => <ProjectRow key={project.id} project={project} selected={project.id === selectedProject} switchProject={switchProject} pinned togglePin={togglePinProject} />)}
        {unpinned.map((project) => <ProjectRow key={project.id} project={project} selected={project.id === selectedProject} switchProject={switchProject} pinned={false} togglePin={togglePinProject} />)}
      </div>
    </aside>
  );
}

function ProjectRow({ project, selected, switchProject, pinned, togglePin }: {
  project: Project;
  selected: boolean;
  switchProject: (id: string) => void;
  pinned: boolean;
  togglePin: (id: string) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => switchProject(project.id)}
      style={{
        display: "flex", alignItems: "center", gap: 6,
        width: "100%", padding: "5px 8px", marginBottom: 2,
        background: selected ? "var(--bg-selected)" : "transparent",
        border: "none", borderRadius: 6, cursor: "pointer",
        color: "var(--text)", fontSize: 12, textAlign: "left",
      }}
      title={project.name}
    >
      <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
        {project.name}
      </span>
      <span
        role="button"
        tabIndex={0}
        title={pinned ? "Unpin project" : "Pin project"}
        onClick={(event) => { event.stopPropagation(); togglePin(project.id); }}
        onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); event.stopPropagation(); togglePin(project.id); } }}
        style={{ color: pinned ? "var(--accent)" : "var(--text-dim)", display: "flex", flexShrink: 0 }}
      >
        {pinned ? <Pin size={12} /> : <PinOff size={12} />}
      </span>
    </button>
  );
}
