"use client";

import { useState } from "react";
import { WorkspaceNav } from "@/components/WorkspaceNav";
import { RolesSettings } from "@/components/settings/RolesSettings";
import { useWorkspace } from "@/components/WorkspaceProvider";
import { useSettingsProfiles } from "@/hooks/useSettingsProfiles";
import { ProjectCreateDialog } from "@/components/ProjectCreateDialog";

export default function RolesPage() {
  const { selectedProject, createProjectOpen, openCreateProject, confirmCreateProject, cancelCreateProject, createProjectBusy } = useWorkspace();
  const settings = useSettingsProfiles();
  const [error, setError] = useState<string | null>(null);

  return (
    <div className="app-shell workspace-nav-page" data-testid="roles-page" style={{ display: "flex", minHeight: 0 }}>
      <div className="sidebar-container sidebar-open" style={{ width: 260, minWidth: 260, display: "flex", flexDirection: "column" }}>
        <WorkspaceNav />
      </div>
      <div className="workspace-content" style={{ flex: 1, display: "flex", flexDirection: "column", overflow: "hidden", minWidth: 0 }}>
        <div style={{ padding: "16px 20px", borderBottom: "1px solid var(--border)", flexShrink: 0 }}>
          <h1 style={{ fontSize: 18, fontWeight: 700, margin: 0 }}>Roles</h1>
          <p style={{ margin: "4px 0 0", color: "var(--text-dim)", fontSize: 12 }}>
            Addressable identities with immutable published versions. Graph-local roles are managed per graph.
          </p>
        </div>
        {error && (
          <div className="error-bar" role="alert" style={{ margin: "0 16px", marginTop: 12 }}>
            <span>{error}</span>
            <button type="button" onClick={() => setError(null)} aria-label="Dismiss error">✕</button>
          </div>
        )}
        <div style={{ flex: 1, minHeight: 0, overflow: "auto" }}>
          <RolesSettings projectId={selectedProject} models={settings.models} setError={setError} />
        </div>
      </div>

      {createProjectOpen && (
        <ProjectCreateDialog
          busy={createProjectBusy}
          error={error}
          onCreate={(name, hostPath) => void confirmCreateProject(name, hostPath)}
          onClose={cancelCreateProject}
        />
      )}
    </div>
  );
}
