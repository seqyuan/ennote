"use client";

import { useState } from "react";
import { WorkspacePageShell } from "@/components/WorkspacePageShell";
import { AgentFlowSettings } from "@/components/settings/AgentFlowSettings";

export default function GraphsPage() {
  const [error, setError] = useState<string | null>(null);
  return (
    <WorkspacePageShell
      title="Graphs"
      description="Global Task graphs. Every published Graph can run in any Session."
      error={error}
      onDismissError={() => setError(null)}
      testId="graphs-page"
    >
      <AgentFlowSettings setError={setError} />
    </WorkspacePageShell>
  );
}
