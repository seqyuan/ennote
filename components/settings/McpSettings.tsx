"use client";

import { useMcpSettings } from "@/hooks/useMcpSettings";
import { McpBindingCard, McpCandidateSection, McpCreateForm } from "./McpPanels";

export function McpSettings({ projectId, setError }: {
  projectId: string | null;
  setError: (value: string | null) => void;
}) {
  const mcp = useMcpSettings(projectId, setError);

  if (!projectId) {
    return <div style={{ fontSize: 12, color: "var(--text-dim)" }}>Open a project to manage MCP servers.</div>;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      {/* Section header */}
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <div>
          <div style={{ fontSize: 13, fontWeight: 700, color: "var(--text)" }}>MCP servers</div>
          <div style={{ fontSize: 11, color: "var(--text-dim)", marginTop: 2 }}>
            Tools-first client. Servers are discovered, tested, then explicitly enabled per project.
          </div>
        </div>
        <button
          type="button"
          onClick={() => mcp.setShowCreate((value) => !value)}
          style={{
            padding: "5px 10px", borderRadius: 6, border: "1px solid var(--border)",
            background: "var(--bg)", color: "var(--text)", fontSize: 12, cursor: "pointer",
          }}
        >
          {mcp.showCreate ? "Cancel" : "+ Add server"}
        </button>
      </div>

      <McpCreateForm mcp={mcp} />

      {/* Bindings */}
      <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
        {mcp.bindings.length === 0 && !mcp.loading && (
          <div style={{ fontSize: 12, color: "var(--text-dim)" }}>No MCP servers bound to this project yet.</div>
        )}
        {mcp.bindings.map((binding) => (
          <McpBindingCard key={binding.id} mcp={mcp} binding={binding} />
        ))}
      </div>

      {/* Discovered candidates (project file, bundled, managed) */}
      <McpCandidateSection mcp={mcp} />
      {mcp.loading && <div style={{ fontSize: 11, color: "var(--text-dim)" }}>Loading…</div>}
    </div>
  );
}
