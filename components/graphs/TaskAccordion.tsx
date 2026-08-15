"use client";

import { ChevronDown, ChevronRight, Trash2 } from "lucide-react";
import type { GraphTask, ModelOption } from "@/components/graphs/types";

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
  const roleMode = Boolean(task.role);
  const selectedModel = models.find((option) => option.ref === task.model);
  const thinkingOptions = selectedModel?.model.supportsThinking
    ? selectedModel.model.supportedThinkingEfforts
    : ["default"];
  const saveField = <K extends keyof GraphTask>(key: K, value: GraphTask[K]) => onSave({ ...task, [key]: value });

  return (
    <section className="graph-task" data-task-id={id}>
      <button type="button" className="graph-task-summary" onClick={onToggle} aria-expanded={expanded}>
        {expanded ? <ChevronDown size={17} /> : <ChevronRight size={17} />}
        <span><strong>{task.name}</strong><code>{id}</code></span>
        <small>{roleMode ? task.role : task.model}{!roleMode && task.thinking && task.thinking !== "default" ? ` · ${task.thinking}` : ""}</small>
      </button>
      {expanded && (
        <div className="graph-task-form">
          <label>Task name
            <input defaultValue={task.name} disabled={busy} onBlur={(event) => saveField("name", event.target.value)} />
          </label>
          <div className="graph-execution-mode" role="group" aria-label="Execution mode">
            <button
              type="button"
              aria-pressed={!roleMode}
              disabled={busy || !models[0]}
              onClick={() => onSave({ name: task.name, model: task.model || models[0]?.ref, thinking: "default", skills: [], goal: task.goal, writes: task.writes, budget: task.budget })}
            >Inline configuration</button>
            <button
              type="button"
              aria-pressed={roleMode}
              disabled={busy}
              onClick={() => onSave({ name: task.name, role: task.role || `local/${id}`, goal: task.goal, writes: task.writes, budget: task.budget })}
            >Use a Role</button>
          </div>
          {roleMode ? (
            <label>Role
              <input defaultValue={task.role} disabled={busy} placeholder="local/alignment or global/reviewer" onBlur={(event) => saveField("role", event.target.value)} />
              <span>Local Roles belong only to this Graph. Global Roles are shared and read-only here.</span>
            </label>
          ) : (
            <>
              <label>Model
                <select value={task.model ?? ""} disabled={busy} onChange={(event) => saveField("model", event.target.value)}>
                  {models.map((option) => <option key={option.ref} value={option.ref}>{option.label} · {option.ref}</option>)}
                </select>
              </label>
              {selectedModel?.model.supportsThinking && (
                <label>Thinking
                  <select value={task.thinking ?? "default"} disabled={busy} onChange={(event) => saveField("thinking", event.target.value as GraphTask["thinking"])}>
                    {thinkingOptions.map((effort) => <option key={effort} value={effort}>{effort}</option>)}
                  </select>
                </label>
              )}
              <label>Skills
                <input
                  defaultValue={(task.skills ?? []).join(", ")}
                  disabled={busy}
                  placeholder="local/alignment, global/report-writing"
                  onBlur={(event) => saveField("skills", event.target.value.split(",").map((value) => value.trim()).filter(Boolean))}
                />
                <span>Use explicit local/ or global/ references.</span>
              </label>
            </>
          )}
          <label className="graph-task-goal">Goal
            <textarea defaultValue={task.goal} disabled={busy} onBlur={(event) => saveField("goal", event.target.value)} />
          </label>
          <details>
            <summary>Advanced</summary>
            <label>Writes
              <input defaultValue={(task.writes ?? []).join(", ")} disabled={busy} onBlur={(event) => saveField("writes", event.target.value.split(",").map((value) => value.trim()).filter(Boolean))} />
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
