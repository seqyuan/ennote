"use client";

import { ChevronDown, ChevronRight, Trash2 } from "lucide-react";
import type { FlowTask } from "@/components/graphs/types";

// The form mounts on the Task's CONTENT, not on its id alone. Changing the id
// would remount on every save and steal focus from the field being edited;
// keying on the content remounts only when the stored Task really changed -- a
// Builder apply, or the Worker normalising the document -- so the form shows the
// new value instead of a stale defaultValue.
export function taskFingerprint(task: FlowTask): string {
  return JSON.stringify(task);
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

export function TaskAccordion({ id, task, expanded, busy, onToggle, onSave, onRemove }: {
  id: string;
  task: FlowTask;
  expanded: boolean;
  busy: boolean;
  onToggle: () => void;
  onSave: (task: FlowTask) => void;
  onRemove: () => void;
}) {
  const saveField = <K extends keyof FlowTask>(key: K, value: FlowTask[K]) => {
    if (!taskFieldChanged(task[key], value)) return;
    onSave({ ...task, [key]: value });
  };

  return (
    <section className="graph-task" data-task-id={id}>
      <button type="button" className="graph-task-summary" onClick={onToggle} aria-expanded={expanded}>
        {expanded ? <ChevronDown size={17} /> : <ChevronRight size={17} />}
        <span><strong>{id}</strong><code>{task.type ?? "hero"}</code></span>
        <small>{task.hero}</small>
      </button>
      {expanded && (
        <div className="graph-task-form">
          <label>Hero
            <input defaultValue={task.hero} placeholder="local/alignment or global/reviewer" onBlur={(event) => saveField("hero", event.target.value)} />
            <span>Local Heroes belong only to this Graph. Global Heroes are shared and read-only here.</span>
          </label>
          <label>Skills
            <input
              defaultValue={(task.skills ?? []).join(", ")}
              placeholder="local/alignment, global/report-writing"
              onBlur={(event) => saveField("skills", event.target.value.split(",").map((value) => value.trim()).filter(Boolean))}
            />
            <span>Use explicit local/ or global/ references.</span>
          </label>
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
