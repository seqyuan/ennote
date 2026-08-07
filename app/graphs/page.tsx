"use client";

import { useState } from "react";
import { WorkspaceNav } from "@/components/WorkspaceNav";
import { AgentFlowSettings } from "@/components/settings/AgentFlowSettings";
import { useWorkspace } from "@/components/WorkspaceProvider";
import { ProjectCreateDialog } from "@/components/ProjectCreateDialog";

export default function GraphsPage() {
  const { selectedProject, createProjectOpen, confirmCreateProject, cancelCreateProject, createProjectBusy } = useWorkspace();
  const [error, setError] = useState<string | null>(null);

  return (
    <div className="app-shell workspace-nav-page" data-testid="graphs-page" style={{ display: "flex", minHeight: 0 }}>
      <div className="sidebar-container sidebar-open" style={{ width: 260, minWidth: 260, display: "flex", flexDirection: "column" }}>
        <WorkspaceNav />
      </div>
      <div className="workspace-content" style={{ flex: 1, display: "flex", flexDirection: "column", overflow: "hidden", minWidth: 0 }}>
        <div style={{ padding: "16px 20px", borderBottom: "1px solid var(--border)", flexShrink: 0 }}>
          <h1 style={{ fontSize: 18, fontWeight: 700, margin: 0 }}>Graphs</h1>
          <p style={{ margin: "4px 0 0", color: "var(--text-dim)", fontSize: 12 }}>
            Agent Flow task graphs. Each graph can define graph-local roles plus reference shared roles.
          </p>
        </div>
        {error && (
          <div className="error-bar" role="alert" style={{ margin: "0 16px", marginTop: 12 }}>
            <span>{error}</span>
            <button type="button" onClick={() => setError(null)} aria-label="Dismiss error">✕</button>
          </div>
        )}
        <div style={{ flex: 1, minHeight: 0, overflow: "auto" }}>
          <AgentFlowSettings projectId={selectedProject} setError={setError} />
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
