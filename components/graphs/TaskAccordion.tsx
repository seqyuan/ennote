"use client";

import { ChevronDown, ChevronRight, Trash2 } from "lucide-react";
import type { GraphTask, ModelOption } from "@/components/graphs/types";

// The form mounts on the Task's CONTENT, not on its id alone. Changing the id
// would remount on every save and steal focus from the field being edited;
// keying on the content remounts only when the stored Task really changed -- a
// Builder apply, or the Worker normalising the document -- so the form shows the
// new value instead of a stale defaultValue.
export function taskFingerprint(task: GraphTask): string {
  return JSON.stringify(task);
}

// A model may not offer the effort the previous model used. Falling back to
// "default" keeps the saved Task runnable instead of hiding the selection and
// failing at Run freeze.
export function thinkingForModel(
  supportsThinking: boolean,
  efforts: readonly GraphTask["thinking"][],
  current: GraphTask["thinking"],
): GraphTask["thinking"] {
  const offered: readonly GraphTask["thinking"][] = supportsThinking ? efforts : ["default"];
  const value = current ?? "default";
  return offered.includes(value) ? value : "default";
}

// A blur is not a request. Treat an unchanged value, and an empty list that was
// already absent, as no change so the editor stops sending a PATCH per field tab.
export function taskFieldChanged(before: unknown, after: unknown): boolean {
  if (Array.isArray(before) || Array.isArray(after)) {
    const left = Array.isArray(before) ? before : [];
    const right = Array.isArray(after) ? after : [];
    return left.length !== right.length || left.some((value, index) => value !== right[index]);
  }
  return before !== after;
}

export function TaskAccordion({ id, task, expanded, busy, models, onToggle, onSave, onRemove }: {
  id: string;
  task: GraphTask;
  expanded: boolean;
  busy: boolean;
  models: ModelOption[];
  onToggle: () => void;
  onSave: (task: GraphTask) => void;
  onRemove: () => void;
}) {
  const heroMode = Boolean(task.hero);
  const selectedModel = models.find((option) => option.ref === task.model);
  const thinkingOptions = selectedModel?.model.supportsThinking
    ? selectedModel.model.supportedThinkingEfforts
    : ["default"];
  const saveField = <K extends keyof GraphTask>(key: K, value: GraphTask[K]) => {
    if (!taskFieldChanged(task[key], value)) return;
    onSave({ ...task, [key]: value });
  };
  // Switching to a model that cannot honour the current effort would leave a
  // selection the Worker hides and the Run then rejects at freeze time.
  const changeModel = (ref: string) => {
    const option = models.find((candidate) => candidate.ref === ref);
    const thinking = thinkingForModel(
      Boolean(option?.model.supportsThinking),
      option?.model.supportedThinkingEfforts ?? [],
      task.thinking,
    );
    if (!taskFieldChanged(task.model, ref) && !taskFieldChanged(task.thinking, thinking)) return;
    onSave({ ...task, model: ref, thinking });
  };

  return (
    <section className="graph-task" data-task-id={id}>
      <button type="button" className="graph-task-summary" onClick={onToggle} aria-expanded={expanded}>
        {expanded ? <ChevronDown size={17} /> : <ChevronRight size={17} />}
        <span><strong>{task.name}</strong><code>{id}</code></span>
        <small>{heroMode ? task.hero : task.model}{!heroMode && task.thinking && task.thinking !== "default" ? ` · ${task.thinking}` : ""}</small>
      </button>
      {expanded && (
        <div className="graph-task-form">
          <label>Task name
            <input defaultValue={task.name} onBlur={(event) => saveField("name", event.target.value)} />
          </label>
          <div className="graph-execution-mode" role="group" aria-label="Execution mode">
            <button
              type="button"
              aria-pressed={!heroMode}
              disabled={busy || !models[0]}
              onClick={() => onSave({ name: task.name, model: task.model || models[0]?.ref, thinking: "default", skills: [], goal: task.goal, writes: task.writes, budget: task.budget })}
            >Inline configuration</button>
            <button
              type="button"
              aria-pressed={heroMode}
              disabled={busy}
              onClick={() => onSave({ name: task.name, hero: task.hero || `local/${id}`, goal: task.goal, writes: task.writes, budget: task.budget })}
            >Use a Hero</button>
          </div>
          {heroMode ? (
            <label>Hero
              <input defaultValue={task.hero} placeholder="local/alignment or global/reviewer" onBlur={(event) => saveField("hero", event.target.value)} />
              <span>Local Heroes belong only to this Graph. Global Heroes are shared and read-only here.</span>
            </label>
          ) : (
            <>
              <label>Model
                <select value={task.model ?? ""} onChange={(event) => changeModel(event.target.value)}>
                  {models.map((option) => <option key={option.ref} value={option.ref}>{option.label} · {option.ref}</option>)}
                </select>
              </label>
              {selectedModel?.model.supportsThinking && (
                <label>Thinking
                  <select value={task.thinking ?? "default"} onChange={(event) => saveField("thinking", event.target.value as GraphTask["thinking"])}>
                    {thinkingOptions.map((effort) => <option key={effort} value={effort}>{effort}</option>)}
                  </select>
                </label>
              )}
              <label>Skills
                <input
                  defaultValue={(task.skills ?? []).join(", ")}
                  placeholder="local/alignment, global/report-writing"
                  onBlur={(event) => saveField("skills", event.target.value.split(",").map((value) => value.trim()).filter(Boolean))}
                />
                <span>Use explicit local/ or global/ references.</span>
              </label>
            </>
          )}
          <label className="graph-task-goal">Goal
            <textarea defaultValue={task.goal} onBlur={(event) => saveField("goal", event.target.value)} />
          </label>
          <details>
            <summary>Advanced</summary>
            <label>Writes
              <input defaultValue={(task.writes ?? []).join(", ")} onBlur={(event) => saveField("writes", event.target.value.split(",").map((value) => value.trim()).filter(Boolean))} />
            </label>
          </details>
          <button type="button" className="graph-task-remove" onClick={onRemove} disabled={busy}>
            <Trash2 size={15} /> Remove Task
          </button>
        </div>
      )}
    </section>
  );
}
