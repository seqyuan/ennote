"use client";

import { RunInspectorPanel } from "../RunInspectorPanel";
import { useT } from "@/components/LocaleProvider";
import { runStatusText } from "@/lib/run-status-copy";
import { useRunMCP } from "@/hooks/useRunMCP";
import { useRunTranscript } from "@/hooks/useRunTranscript";
import { useRunPromptComposition } from "@/hooks/useRunPromptComposition";
import type { PermissionMode } from "@/lib/permission-mode";

/**
 * The Status channel: the run inspector plus the session's headline facts.
 *
 * The three inspector fetches live here rather than in the panel shell, so they
 * only run while this channel is the one on screen. Passing `null` clears them;
 * the hooks have no target and issue no request.
 */
export function StatusChannel(props: {
  runId: string | null;
  selectedSession: string | null;
  sessionTitle: string;
  activeRun: string | null;
  status: string;
  permissionMode: PermissionMode;
}) {
  const { runId, selectedSession, sessionTitle, activeRun, status, permissionMode } = props;
  const prompt = useRunPromptComposition(runId);
  const mcp = useRunMCP(runId);
  const transcript = useRunTranscript(runId);

  return (
    <div className="right-panel-tools">
      <RunInspectorPanel
        runId={runId}
        composition={prompt.composition}
        mcp={mcp.frozen}
        transcript={transcript}
        loading={prompt.loading}
        error={prompt.error}
      />
      <RightPanelSummary
        selectedSession={selectedSession}
        sessionTitle={sessionTitle}
        activeRun={activeRun}
        status={status}
        permissionMode={permissionMode}
      />
    </div>
  );
}

// The summary reads Worker facts, so the run status must be rendered through
// the copy layer: the wire form is `code` or `code\u001Fdetail`, never UI text.
export function RightPanelSummary({ selectedSession, sessionTitle, activeRun, status, permissionMode }: {
  selectedSession: string | null;
  sessionTitle: string;
  activeRun: string | null;
  status: string;
  permissionMode: PermissionMode;
}) {
  const t = useT();
  return (
    <div className="right-panel-summary">
      {selectedSession ? (
        <>
          <div>Session: {sessionTitle}</div>
          <div>Active run: {activeRun || "none"}</div>
          <div>Status: {status ? runStatusText(status, t) : "idle"}</div>
          <div>Permission: {permissionMode}</div>
        </>
      ) : (
        <div>No session selected</div>
      )}
    </div>
  );
}
