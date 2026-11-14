"use client";

import { ChevronRight } from "lucide-react";
import type { FlowDefinition } from "@/components/graphs/types";

function levelsFor(definition: FlowDefinition): string[][] {
  const remaining = new Set(Object.keys(definition.tasks));
  const completed = new Set<string>();
  const levels: string[][] = [];
  while (remaining.size > 0) {
    const level = [...remaining].filter((id) => (definition.tasks[id].depends ?? []).every((dependency) => completed.has(dependency))).sort();
    if (level.length === 0) return [...levels, [...remaining].sort()];
    levels.push(level);
    level.forEach((id) => { remaining.delete(id); completed.add(id); });
  }
  return levels;
}

export function GraphDependencies({ definition, busy, onOpenTask, onChange }: {
  definition: FlowDefinition;
  busy: boolean;
  onOpenTask: (id: string) => void;
  onChange: (taskId: string, dependencies: string[]) => void;
}) {
  const taskIds = Object.keys(definition.tasks).sort();
  if (taskIds.length === 0) {
    return <div className="graph-empty-state">Add a Task to start the Graph.</div>;
  }
  return (
    <div className="graph-dependency-editor">
      <div className="graph-levels" aria-label="Task dependency levels">
        {levelsFor(definition).map((level, index) => (
          <div className="graph-level" key={index}>
            <span className="graph-level-label">Level {index + 1}</span>
            <div>{level.map((id) => (
              <button key={id} type="button" onClick={() => onOpenTask(id)}>{id}</button>
            ))}</div>
            {index < levelsFor(definition).length - 1 && <ChevronRight size={16} aria-hidden />}
          </div>
        ))}
      </div>
      <div className="graph-dependency-list">
        {taskIds.map((id) => {
          const selected = new Set(definition.tasks[id].depends ?? []);
          return (
            <fieldset key={id} disabled={busy}>
              <legend>{id}</legend>
              <div>
                {taskIds.filter((candidate) => candidate !== id).map((candidate) => (
                  <label key={candidate}>
                    <input
                      type="checkbox"
                      checked={selected.has(candidate)}
                      onChange={(event) => {
                        const next = new Set(selected);
                        if (event.target.checked) next.add(candidate); else next.delete(candidate);
                        onChange(id, [...next].sort());
                      }}
                    />
                    {candidate}
                  </label>
                ))}
                {taskIds.length === 1 && <span>No other Tasks</span>}
              </div>
            </fieldset>
          );
        })}
      </div>
    </div>
  );
}
