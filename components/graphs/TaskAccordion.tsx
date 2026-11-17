"use client";

import { ChevronDown, ChevronRight, Trash2 } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import type { FlowTask } from "@/components/graphs/types";

/**
 * A comma-separated list field that types like a text input.
 *
 * The task holds a list, but re-parsing and re-joining on every keystroke would
 * eat the comma the user is in the middle of typing ("a, " becomes ["a"] and
 * renders back as "a"). The field therefore keeps the raw text locally and only
 * re-syncs when the draft's list changes from somewhere else — a Worker
 * normalisation or a Builder apply — never from its own echo.
 */
function ListField({ id, label, value, placeholder, hint, onChange }: {
  id: string;
  label: string;
  value: string[];
  placeholder: string;
  hint?: string;
  onChange: (next: string[]) => void;
}) {
  const parse = (text: string) => text.split(",").map((entry) => entry.trim()).filter(Boolean);
  const [text, setText] = useState(value.join(", "));
  const committed = useRef(value.join("\u0000"));

  useEffect(() => {
    const incoming = value.join("\u0000");
    if (incoming === committed.current) return;
    committed.current = incoming;
    setText(value.join(", "));
  }, [value]);

  return (
    <label>{label}
      <input
        id={id}
        value={text}
        placeholder={placeholder}
        onChange={(event) => {
          setText(event.target.value);
          committed.current = parse(event.target.value).join("\u0000");
          onChange(parse(event.target.value));
        }}
        onBlur={() => setText(parse(text).join(", "))}
      />
      {hint && <span>{hint}</span>}
    </label>
  );
}

/**
 * One Task's form, controlled by the editor's draft.
 *
 * It writes nothing itself: every change goes to the parent, which turns it into a
 * queued op. The previous version saved on blur and returned silently when a save
 * was already running, so an edit typed during a save was dropped with no error
 * and no marker; there is no equivalent state here, because an edit is a queue
 * entry rather than a request.
 */
export function TaskAccordion({ id, task, expanded, onToggle, onChange, onRemove, heroPanel }: {
  id: string;
  task: FlowTask;
  expanded: boolean;
  onToggle: () => void;
  onChange: (task: FlowTask) => void;
  onRemove: () => void;
  /**
   * The Graph-local Hero editor for this Task, when its Hero has one. Passed as a
   * node rather than as data: the form is presentational and knows nothing about
   * the Hero endpoints.
   */
  heroPanel?: ReactNode;
}) {
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
            <input value={task.hero ?? ""} placeholder="local/alignment or global/reviewer"
              onChange={(event) => onChange({ ...task, hero: event.target.value })} />
            <span>Local Heroes belong only to this Graph. Global Heroes are shared and read-only here.</span>
          </label>
          <ListField id={`${id}-skills`} label="Skills" value={task.skills ?? []}
            placeholder="local/alignment, global/report-writing"
            hint="Use explicit local/ or global/ references."
            onChange={(skills) => onChange({ ...task, skills })} />
          <label className="graph-task-goal">Goal
            <textarea value={task.goal ?? ""} onChange={(event) => onChange({ ...task, goal: event.target.value })} />
          </label>
          {heroPanel}
          <details>
            <summary>Advanced</summary>
            <ListField id={`${id}-writes`} label="Writes" value={task.writes ?? []}
              placeholder="data/**, reports/*.md"
              onChange={(writes) => onChange({ ...task, writes })} />
          </details>
          <button type="button" className="graph-task-remove" onClick={onRemove}>
            <Trash2 size={15} /> Remove Task
          </button>
        </div>
      )}
    </section>
  );
}
