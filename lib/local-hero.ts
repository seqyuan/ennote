import type { components } from "@/lib/worker-api.gen";

/** The Hero document shape the Worker accepts for a draft. */
export type PortableHeroDocument = components["schemas"]["PortableHeroDocument"];

// The Hero handle is a directory name, and the Worker's object-id rule is
// stricter than a Graph task id: it must start with a letter and is limited to
// 32 characters. Deriving the handle from the task id keeps "one Task, one
// Graph-local Hero" true without asking the reader for a second name.
const heroHandlePattern = /^[a-z][a-z0-9_-]{1,31}$/;

/**
 * localHeroHandleFor reports the handle a Task's new Hero should use.
 *
 * It returns null when the task id cannot be a handle, so the form can refuse
 * before writing: a Hero whose handle fails validation would be created and then
 * rejected on save, leaving the reader with a Task that references nothing.
 */
export function localHeroHandleFor(taskID: string): string | null {
  const trimmed = taskID.trim();
  return heroHandlePattern.test(trimmed) ? trimmed : null;
}

/**
 * defaultLocalHero is the draft a new Graph-private Hero starts from.
 *
 * The context policy is task_only because a Graph task's child Run is always
 * task-only — the kernel refuses anything else at freeze time, so a default of
 * "room" would produce a Hero that cannot run the Task it was created for.
 */
export function defaultLocalHero(handle: string, name: string, modelRef: string): PortableHeroDocument {
  return {
    schemaVersion: 1,
    handle,
    name,
    description: "",
    positioning: "",
    icon: "bot",
    color: "neutral",
    model: { ref: modelRef, thinkingEffort: "default", fallbacks: [] },
    skills: [],
    authority: "read_only",
    permissionCeiling: "discuss",
    allowedTools: ["read", "ls", "grep", "find"],
    context: { defaultMode: "task_only", allowedModes: ["task_only"], ownExecutionContinuity: "none" },
    delegation: {
      admission: "auto_within_budget",
      allowedCallerKinds: ["host"],
      allowedStrategies: ["single"],
      maxInvocationsPerParentRun: 1,
      maxConcurrentInstances: 1,
      budgetCeiling: {
        maxModelCalls: 4,
        maxToolCalls: 8,
        maxTotalTokens: 20000,
        maxOutputTokens: 4000,
        maxCostUsdMicros: 100000,
        maxWallTimeMs: 120000,
      },
    },
    outputContract: "text-v1",
    maxLoopIterations: 8,
    prompt: "Execute the Task goal independently. Distinguish evidence from assumptions and submit a concise result.",
  };
}

/** localHeroId returns the handle a `local/<id>` reference names, or null. */
export function localHeroId(reference: string | undefined): string | null {
  if (!reference) return null;
  const trimmed = reference.trim();
  if (!trimmed.startsWith("local/")) return null;
  const handle = trimmed.slice("local/".length).trim();
  return handle === "" ? null : handle;
}

/** parseToolList turns the comma-separated field into the document's tool list. */
export function parseToolList(text: string): string[] {
  return text.split(",").map((entry) => entry.trim()).filter(Boolean);
}
