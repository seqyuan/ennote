// Agent Flow editor pure logic: form draft <-> YAML contract and budget
// suggestions. Kept dependency-free so the UI, unit tests, and e2e mocks
// share one canonical YAML serializer.

export type FlowTaskKind = "role" | "check" | "terminal";

export interface TaskDraft {
  id: string;
  name: string;
  kind: FlowTaskKind;
  role: string;
  skills: string[];
  goal: string;
  depends: string[];
  budgetTokens: string;
  command: string;
  outputPort: string;
}

export interface FlowDraft {
  id: string;
  description: string;
  inputs: { name: string; type: string; required: boolean }[];
  outputs: { name: string; type: string }[];
  maxTotalTokens: string;
  tasks: TaskDraft[];
}

export function emptyTask(name: string): TaskDraft {
  return {
    id: `task-${Math.random().toString(36).slice(2)}`,
    name,
    kind: "role",
    role: "",
    skills: [],
    goal: "",
    depends: [],
    budgetTokens: "",
    command: "",
    outputPort: "",
  };
}

export function emptyDraft(id: string): FlowDraft {
  return {
    id,
    description: "",
    inputs: [{ name: "target", type: "path", required: true }],
    outputs: [{ name: "report", type: "string" }],
    maxTotalTokens: "",
    tasks: [emptyTask("producer")],
  };
}

type RawFlowDefinition = {
  description?: string;
  inputs?: Record<string, { type?: string; required?: boolean }>;
  outputs?: Record<string, { type?: string }>;
  budget?: { maxTotalTokens?: number };
  tasks?: Record<string, RawTask>;
};

type RawTask = {
  type?: string;
  role?: string;
  skills?: string[];
  goal?: string;
  depends?: string[];
  budget?: { tokens?: number };
  command?: string;
  terminal?: { status?: string; output?: string };
};

export function flowToDraft(def: RawFlowDefinition | undefined, id: string): FlowDraft {
  if (!def) return emptyDraft(id);
  const tasks = Object.entries(def.tasks ?? {}).map(([name, task]) => {
    const kind: FlowTaskKind = task.terminal ? "terminal" : task.type === "check" ? "check" : "role";
    return {
      id: `task-${Math.random().toString(36).slice(2)}`, name,
      kind, role: task.role ?? "", skills: task.skills ?? [], goal: task.goal ?? "",
      depends: task.depends ?? [], budgetTokens: String(task.budget?.tokens ?? ""),
      command: task.command ?? "", outputPort: task.terminal?.output ?? "",
    };
  });
  return {
    id, description: def.description ?? "",
    inputs: Object.entries(def.inputs ?? {}).map(([name, port]) => ({
      name, type: port.type ?? "string", required: !!port.required,
    })),
    outputs: Object.entries(def.outputs ?? {}).map(([name, port]) => ({
      name, type: port.type ?? "string",
    })),
    maxTotalTokens: String(def.budget?.maxTotalTokens ?? ""),
    tasks,
  };
}

const jsonQuote = (value: string) => JSON.stringify(value ?? "");

/** Serializes the form draft to the snake_case YAML contract. */
export function draftToYAML(draft: FlowDraft): string {
  const lines: string[] = [];
  lines.push("schemaVersion: 1");
  lines.push(`id: ${draft.id || "unnamed-flow"}`);
  if (draft.description) lines.push(`description: ${jsonQuote(draft.description)}`);
  if (draft.inputs.length > 0) {
    lines.push("inputs:");
    for (const input of draft.inputs) {
      lines.push(`  ${input.name}: {type: ${input.type}, required: ${input.required}}`);
    }
  }
  if (draft.outputs.length > 0) {
    lines.push("outputs:");
    for (const output of draft.outputs) {
      lines.push(`  ${output.name}: {type: ${output.type}}`);
    }
  }
  lines.push("budget:");
  lines.push(`  max_total_tokens: ${Number(draft.maxTotalTokens) || 0}`);
  lines.push("tasks:");
  for (const task of draft.tasks) {
    lines.push(`  ${task.name}:`);
    if (task.kind === "check") {
      lines.push("    type: check");
      lines.push(`    command: ${jsonQuote(task.command)}`);
    } else if (task.kind === "terminal") {
      lines.push(`    terminal: {status: success, output: ${task.outputPort || "report"}}`);
      lines.push(`    output: ${task.outputPort || "report"}`);
    } else {
      lines.push(`    role: ${task.role}`);
      if (task.skills.length > 0) lines.push(`    skills: [${task.skills.join(", ")}]`);
      lines.push(`    goal: ${jsonQuote(task.goal)}`);
    }
    if (task.depends.length > 0) lines.push(`    depends: [${task.depends.join(", ")}]`);
    if (task.kind === "role" && task.budgetTokens) {
      lines.push(`    budget: {tokens: ${Number(task.budgetTokens) || 0}}`);
    }
  }
  return lines.join("\n") + "\n";
}

/** Suggested flow budget = 1.25x sum of task token budgets (min 10,000). */
export function suggestedBudget(draft: FlowDraft): number {
  const sum = draft.tasks.reduce((acc, task) => {
    const tokens = Number(task.budgetTokens);
    return acc + (Number.isFinite(tokens) ? tokens : 0);
  }, 0);
  const suggested = Math.ceil(sum * 1.25);
  return Math.max(suggested, 10000);
}

/** Variable insert helpers: the editor only offers in-scope variables. */
export function variableChips(task: TaskDraft, draft: FlowDraft): string[] {
  const chips: string[] = [];
  for (const input of draft.inputs) chips.push(`{inputs.${input.name}}`);
  const index = draft.tasks.findIndex((t) => t.id === task.id);
  const previous = index < 0 ? [] : draft.tasks.slice(0, index);
  for (const prev of previous) chips.push(`{task.${prev.name}.output}`);
  chips.push("{flow.vars.mode}");
  return chips;
}
