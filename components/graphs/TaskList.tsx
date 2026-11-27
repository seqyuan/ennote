"use client";

import { LocalHeroPanel } from "@/components/graphs/LocalHeroPanel";
import { TaskAccordion } from "@/components/graphs/TaskAccordion";
import type { FlowDefinition, FlowTask, ModelOption } from "@/components/graphs/types";
import { localHeroId } from "@/lib/local-hero";
import { useT } from "@/components/LocaleProvider";

/**
 * The Task list of the Graph editor.
 *
 * It owns one piece of knowledge the accordion should not: a Task whose Hero is
 * `local/<id>` gets the editor for that Hero, and every other Task does not. The
 * Hero document lives behind its own endpoints, so the panel is mounted with a key
 * per (Graph, Hero) — switching Tasks or Graphs remounts it rather than leaving a
 * stale document on screen.
 */
export function TaskList({ graphId, definition, expanded, models, setError, onHeroSaved, onToggle, onChange, onRemove }: {
  graphId: string;
  definition: FlowDefinition;
  expanded: string | null;
  models: ModelOption[];
  setError: (value: string | null) => void;
  onHeroSaved: () => void;
  onToggle: (id: string) => void;
  onChange: (id: string, task: FlowTask) => void;
  onRemove: (id: string) => void;
}) {
  const t = useT();
  const ids = Object.keys(definition.tasks).sort();
  if (ids.length === 0) {
    return <div className="graph-empty-state">{t("graph.task.empty")}</div>;
  }
  return (
    <div className="graph-task-list">
      {ids.map((id) => {
        const heroId = localHeroId(definition.tasks[id].hero);
        return (
          <TaskAccordion
            key={id}
            id={id}
            task={definition.tasks[id]}
            expanded={expanded === id}
            onToggle={() => onToggle(id)}
            onChange={(task) => onChange(id, task)}
            onRemove={() => onRemove(id)}
            heroPanel={heroId === null ? undefined : (
              <LocalHeroPanel
                key={`${graphId}:${heroId}`}
                graphId={graphId}
                heroId={heroId}
                models={models}
                onError={setError}
                onSaved={onHeroSaved}
              />
            )}
          />
        );
      })}
    </div>
  );
}
