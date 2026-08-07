export type DisplayRiskClass = "read_only" | "local_write" | "shell" | "external" | "delegation" | "sensitive";
export type ToolActivityState = "pending" | "running" | "completed" | "failed" | "rejected" | "interrupted";

const readOnlyTools = new Set(["read", "ls", "list", "grep", "search", "find", "search_compacted_history", "todo"]);
const writeTools = new Set(["write", "edit", "publish_artifact"]);
const shellTools = new Set(["bash", "exec"]);
const externalTools = new Set(["http", "http_request", "fetch", "web_search", "web_fetch"]);
const secretKey = /api[_-]?key|token|secret|password|credential|authorization|cookie/i;

export interface ToolSummary {
  label: string;
  target: string;
  detail?: string;
}

export function classifyDisplayRisk(toolName: string): DisplayRiskClass {
  const name = toolName.toLowerCase();
  if (readOnlyTools.has(name)) return "read_only";
  if (writeTools.has(name)) return "local_write";
  if (shellTools.has(name)) return "shell";
  if (externalTools.has(name)) return "external";
  if (name === "delegate_tasks" || name === "delegate_roles") return "delegation";
  return "sensitive";
}

export function redactToolArguments(value: unknown, depth = 0): unknown {
  if (depth > 8) return "[nested value omitted]";
  if (Array.isArray(value)) return value.slice(0, 50).map(item => redactToolArguments(item, depth + 1));
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value as Record<string, unknown>).slice(0, 80).map(([key, item]) => [
    key,
    secretKey.test(key) ? "[redacted]" : redactToolArguments(item, depth + 1),
  ]));
}

export function summarizeToolCall(toolName: string, rawArguments: unknown): ToolSummary {
  const args = objectArguments(redactToolArguments(rawArguments));
  const path = stringValue(args.path ?? args.filePath ?? args.directory ?? args.cwd);
  switch (toolName.toLowerCase()) {
    case "read": {
      const offset = numberValue(args.offset);
      const limit = numberValue(args.limit);
      const detail = offset !== undefined && limit !== undefined ? `lines ${offset}-${offset + limit - 1}` : undefined;
      return { label: "Read file", target: path || "Workspace file", detail };
    }
    case "ls":
    case "list":
      return { label: "List directory", target: path || "Workspace" };
    case "grep":
    case "search":
      return { label: "Search text", target: stringValue(args.query ?? args.pattern) || "Search", detail: path || undefined };
    case "find":
      return { label: "Find files", target: stringValue(args.pattern ?? args.name) || "Files", detail: path || undefined };
    case "write":
      return { label: "Write file", target: path || "Workspace file", detail: contentSize(args.content) };
    case "edit":
      return { label: "Edit file", target: path || "Workspace file", detail: contentSize(args.newText ?? args.replacement) };
    case "publish_artifact":
      return { label: "Publish artifact", target: stringValue(args.name) || path || "Workspace file" };
    case "bash":
    case "exec":
      return { label: "Run command", target: oneLine(stringValue(args.command ?? args.cmd) || "Shell command", 180), detail: path || undefined };
    case "web_fetch":
      return { label: "Fetch external page", target: oneLine(stringValue(args.url) || "External URL", 180) };
    case "http":
    case "http_request":
    case "fetch":
      return { label: "External request", target: oneLine(stringValue(args.url) || "External service", 180) };
    case "search_compacted_history":
      return { label: "Search compacted history", target: oneLine(stringValue(args.query) || "Session history", 180) };
    case "delegate_tasks":
    case "delegate_roles": { // legacy alias stays readable in history
      const tasks = Array.isArray(args.tasks)
        ? args.tasks
        : (Array.isArray(args.delegations) ? args.delegations : []);
      const roles = tasks.map(item => objectArguments(item))
        .map(item => stringValue(item.role ?? item.roleHandle)).filter(Boolean);
      return {
        label: "Delegate tasks",
        target: tasks.length === 1 ? "1 task" : `${tasks.length} tasks`,
        detail: roles.length ? roles.map(handle => `@${handle}`).join(", ") : undefined,
      };
    }
    case "todo": {
      const todos = Array.isArray(rawArguments && typeof rawArguments === "object" && "todos" in (rawArguments as Record<string, unknown>) ? (rawArguments as { todos: unknown[] }).todos : undefined) ? (rawArguments as { todos: unknown[] }).todos : [];
      const completed = todos.filter((item: unknown) => item && typeof item === "object" && (item as Record<string, unknown>).status === "completed").length;
      return { label: "Update task list", target: `${completed}/${todos.length} completed` };
    }
    default: {
      // MCP tools expose as {server}__{remoteTool}. Split so the timeline
      // shows the server and the remote tool name independently.
      if (toolName.includes("__")) {
        const separator = toolName.indexOf("__");
        const server = toolName.slice(0, separator);
        const remote = toolName.slice(separator + 2);
        return { label: remote || toolName, target: `MCP · ${server}` };
      }
      return { label: toolName || "Unknown tool", target: "Sensitive tool call" };
    }
  }
}

export function boundedToolOutput(value: string, limit = 4000): string {
  if (value.length <= limit) return value;
  return `${value.slice(0, limit)}\n[${value.length - limit} characters omitted]`;
}

export function defaultToolExpanded(risk: DisplayRiskClass, state: ToolActivityState): boolean {
  if (state !== "completed") return true;
  return risk !== "read_only";
}

function objectArguments(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function contentSize(value: unknown): string | undefined {
  return typeof value === "string" ? `${value.length.toLocaleString()} characters` : undefined;
}

function oneLine(value: string, limit: number): string {
  const normalized = value.replace(/\s+/g, " ").trim();
  return normalized.length <= limit ? normalized : `${normalized.slice(0, limit - 1)}…`;
}
