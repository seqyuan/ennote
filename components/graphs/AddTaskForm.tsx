"use client";

import { Plus } from "lucide-react";
import { useEffect, useState } from "react";
import type { FlowTask } from "@/components/graphs/types";
import { defaultLocalHero, localHeroHandleFor } from "@/lib/local-hero";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type GlobalHeroSummary = components["schemas"]["GlobalHeroSummary"];

/**
 * The Add Task form.
 *
 * It offers the two Hero sources that actually exist. A shared Hero is referenced
 * by handle; a Graph-private Hero is created by the same mutation that creates the
 * Task, so the reference can never point at a document nobody wrote. (There is no
 * "inline Hero" choice: the Worker refuses a Task with no Hero, and the inline
 * synthesis path in graphrun is not reachable.)
 */
export function AddTaskForm({ takenIds, defaultGoal, modelRef, onAdd, onCancel }: {
  takenIds: string[];
  defaultGoal: string;
  modelRef: string;
  onAdd: (task: { id: string; value: FlowTask; localHero?: Record<string, unknown> }) => void;
  onCancel: () => void;
}) {
  const [taskID, setTaskID] = useState("");
  const [mode, setMode] = useState<"local" | "global">("local");
  const [heroes, setHeroes] = useState<GlobalHeroSummary[] | null>(null);
  const [heroID, setHeroID] = useState("");
  const [error, setError] = useState<string | null>(null);

  // Fetched when the form opens rather than on mount: most Graph edits never add
  // a Task, and the catalog is a directory read.
  useEffect(() => {
    let cancelled = false;
    void apiFetch<GlobalHeroSummary[]>("/v1/global-heroes")
      .then((list) => { if (!cancelled) setHeroes(list.filter((hero) => !hero.error)); })
      .catch(() => { if (!cancelled) setHeroes([]); });
    return () => { cancelled = true; };
  }, []);

  const submit = () => {
    const id = taskID.trim();
    if (!id) return;
    if (takenIds.includes(id)) {
      setError(`“${id}” is already a Task in this Graph.`);
      return;
    }
    if (mode === "global") {
      if (!heroID) {
        setError("Choose a shared Hero, or create a Graph-local one.");
        return;
      }
      onAdd({ id, value: { hero: `global/${heroID}`, goal: defaultGoal } });
      return;
    }
    const handle = localHeroHandleFor(id);
    if (!handle) {
      setError("A Graph-local Hero needs an id that starts with a letter and uses only a-z, 0-9, - or _ (2–32 characters).");
      return;
    }
    if (!modelRef) {
      setError("Create an active model before adding a Task that needs its own Hero.");
      return;
    }
    onAdd({
      id,
      value: { hero: `local/${handle}`, goal: defaultGoal },
      localHero: defaultLocalHero(handle, id, modelRef) as unknown as Record<string, unknown>,
    });
  };

  return (
    <div className="graph-add-task" role="group" aria-label="Add Task">
      <input autoFocus value={taskID} onChange={(event) => { setTaskID(event.target.value); setError(null); }} placeholder="task_id" />
      <label>
        <input type="radio" name="graph-add-task-hero" checked={mode === "local"} onChange={() => { setMode("local"); setError(null); }} />
        New Graph-local Hero
      </label>
      <label>
        <input type="radio" name="graph-add-task-hero" checked={mode === "global"} onChange={() => { setMode("global"); setError(null); }} />
        Shared Hero
      </label>
      {mode === "global" && (
        <select value={heroID} onChange={(event) => { setHeroID(event.target.value); setError(null); }}>
          <option value="">{heroes === null ? "Loading Heroes…" : "Choose a Hero"}</option>
          {(heroes ?? []).map((hero) => <option key={hero.id} value={hero.id}>{hero.name} ({hero.id})</option>)}
        </select>
      )}
      {error && <span role="alert">{error}</span>}
      <button type="button" onClick={submit}><Plus size={14} /> Add</button>
      <button type="button" onClick={onCancel}>Cancel</button>
    </div>
  );
}
