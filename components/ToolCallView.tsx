"use client";

import { useMemo } from "react";
import {
  CircleAlert, CircleCheck, Clock3, FilePenLine, FileText, FolderOpen, Globe2,
  Search, ShieldAlert, Terminal, Users, XCircle,
} from "lucide-react";
import { ArtifactView } from "@/components/ArtifactView";
import { NestedActivityPanel } from "@/components/NestedActivityPanel";
import type { ToolActivity } from "@/lib/chat-messages";
import {
  boundedToolOutput, defaultToolExpanded, redactToolArguments, summarizeToolCall,
} from "@/lib/tool-presentation";

const riskLabels = {
  read_only: "Read only",
  local_write: "Local write",
  shell: "Shell",
  external: "External",
  delegation: "Delegation",
  sensitive: "Sensitive",
};

function isDelegationTool(name: string): boolean {
  return name === "delegate_tasks" || name === "delegate_roles";
}

// DelegationTaskList renders the model-authored task graph of a delegate_tasks
// call (task name, role, optional dependency chain) instead of raw arguments
// JSON. Legacy delegate_roles history is still parsed for display.
function DelegationTaskList({ arguments: rawArguments }: { arguments?: unknown }) {
  const tasks = useMemo(() => {
    const args = (rawArguments && typeof rawArguments === "object"
      ? rawArguments as Record<string, unknown>
      : {}) as Record<string, unknown>;
    const list = Array.isArray(args.tasks) ? args.tasks
      : (Array.isArray(args.delegations) ? args.delegations : []);
    return list.map((entry) => {
      const task = (entry && typeof entry === "object" ? entry as Record<string, unknown> : {});
      const role = String(task.role ?? task.roleHandle ?? "");
      const depends = Array.isArray(task.depends)
        ? (task.depends as unknown[]).map(String).filter(Boolean) : [];
      return { name: String(task.name ?? ""), role, depends };
    }).filter(task => task.name || task.role);
  }, [rawArguments]);
  if (tasks.length === 0) return null;
  return <div className="delegation-task-list" role="list">
    {tasks.map((task, index) => <div className="delegation-task" key={`${task.name}-${index}`} role="listitem">
      <span className="delegation-task-name">{task.name || "(task)"}</span>
      {task.role && <span className="delegation-task-role">@{task.role}</span>}
      {task.depends.length > 0 && <span className="delegation-task-depends">after {task.depends.join(", ")}</span>}
    </div>)}
  </div>;
}

export function ToolCallView({ activity, sessionId }: { activity: ToolActivity; sessionId: string }) {
  const summary = summarizeToolCall(activity.toolName, activity.arguments);
  const argumentsText = activity.arguments
    ? boundedToolOutput(JSON.stringify(redactToolArguments(activity.arguments), null, 2), 2200)
    : activity.argumentsFragment ? "Arguments were incomplete." : "No arguments recorded.";
  const output = activity.result ? boundedToolOutput(activity.result.content) : "";

  return <div className="tool-activity-entry">
    <details className="tool-activity" data-risk={activity.riskClass} data-state={activity.state}
      data-tool-call-id={activity.toolCallId} open={defaultToolExpanded(activity.riskClass, activity.state) || undefined}>
      <summary>
      <span className="tool-icon">{toolIcon(activity.toolName)}</span>
      <span className="tool-summary">
        <strong>{summary.label}</strong>
        <span className="tool-target">{summary.target}</span>
        {summary.detail && <span className="tool-detail">{summary.detail}</span>}
      </span>
      <span className="tool-risk">{riskLabels[activity.riskClass]}</span>
      <span className="tool-state">{stateIcon(activity.state)}{stateLabel(activity.state)}</span>
    </summary>
    <div className="tool-activity-details">
      {isDelegationTool(activity.toolName)
        ? <DelegationTaskList arguments={activity.arguments} />
        : <details className="tool-detail-disclosure">
          <summary>Arguments</summary>
          <pre>{argumentsText}</pre>
        </details>}
      {activity.result && <details className="tool-detail-disclosure" open={activity.state !== "completed" || undefined}>
        <summary>{activity.result.isError ? "Error output" : "Result"}</summary>
        <pre>{output || "No output"}</pre>
      </details>}
      </div>
    </details>
    {(activity.toolName === "delegate_tasks" || activity.toolName === "delegate_roles") && activity.runId &&
      <NestedActivityPanel parentRunId={activity.runId} toolCallId={activity.toolCallId} />}
    {(activity.result?.artifacts.length ?? 0) > 0 && <div className="tool-artifact-results">
      {activity.result?.artifacts.map(artifact => <ArtifactView sessionId={sessionId} artifact={artifact} key={artifact.artifactId} />)}
    </div>}
  </div>;
}

function toolIcon(name: string) {
  const props = { size: 15, "aria-hidden": true } as const;
  switch (name.toLowerCase()) {
    case "read": return <FileText {...props} />;
    case "ls":
    case "list": return <FolderOpen {...props} />;
    case "grep":
    case "search":
    case "find":
    case "search_compacted_history": return <Search {...props} />;
    case "write":
    case "edit":
    case "publish_artifact": return <FilePenLine {...props} />;
    case "bash":
    case "exec": return <Terminal {...props} />;
    case "delegate_tasks":
    case "delegate_roles": return <Users {...props} />;
    case "http":
    case "http_request":
    case "fetch":
    case "web_search": return <Globe2 {...props} />;
    default: return <ShieldAlert {...props} />;
  }
}

function stateIcon(state: ToolActivity["state"]) {
  const props = { size: 14, "aria-hidden": true } as const;
  if (state === "completed") return <CircleCheck {...props} />;
  if (state === "pending" || state === "running") return <Clock3 {...props} />;
  if (state === "rejected") return <XCircle {...props} />;
  return <CircleAlert {...props} />;
}

function stateLabel(state: ToolActivity["state"]): string {
  if (state === "completed") return "Done";
  if (state === "pending") return "Pending";
  if (state === "running") return "Running";
  if (state === "rejected") return "Rejected";
  if (state === "interrupted") return "Interrupted";
  return "Failed";
}
