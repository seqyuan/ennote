"use client";

import { useMcpSettings } from "@/hooks/useMcpSettings";
import { McpBindingCard, McpCandidateSection, McpCreateForm } from "./McpPanels";
import styles from "./McpPanels.module.css";

export function McpSettings({ projectId, setError }: {
  projectId: string | null;
  setError: (value: string | null) => void;
}) {
  const mcp = useMcpSettings(projectId, setError);

  if (!projectId) {
    return <div className={styles.emptyText}>Open a project to manage MCP servers.</div>;
  }

  return (
    <div className={styles.settingsRoot}>
      {/* Section header */}
      <div className={styles.settingsHeader}>
        <div>
          <div className={styles.settingsTitle}>MCP servers</div>
          <div className={styles.settingsDescription}>
            Tools-first client. Servers are discovered, tested, then explicitly enabled per project.
          </div>
        </div>
        <button
          type="button"
          onClick={() => mcp.setShowCreate((value) => !value)}
          className={styles.addButton}
        >
          {mcp.showCreate ? "Cancel" : "+ Add server"}
        </button>
      </div>

      <McpCreateForm mcp={mcp} />

      {/* Bindings */}
      <div className={styles.bindings}>
        {mcp.bindings.length === 0 && !mcp.loading && (
          <div className={styles.emptyText}>No MCP servers bound to this project yet.</div>
        )}
        {mcp.bindings.map((binding) => (
          <McpBindingCard key={binding.id} mcp={mcp} binding={binding} />
        ))}
      </div>

      {/* Discovered candidates (project file, bundled, managed) */}
      <McpCandidateSection mcp={mcp} />
      {mcp.loading && <div className={styles.loadingText}>Loading…</div>}
    </div>
  );
}
