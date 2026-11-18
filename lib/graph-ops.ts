// The Graph editor's write model.
//
// The draft is the server definition plus the ops that have not been sent yet —
// there is no third source of truth (plan v3 §2.1). Every user edit becomes
// exactly one op, so saving is a queue drain rather than a document diff: a
// successful PATCH makes the Worker's normalised answer the new base, and
// whatever is still queued is replayed on top of it. That is what stops the
// editor from getting stuck "dirty" when the Worker normalises a value (it
// trims names, canonicalises task types), which a diff-based editor cannot
// resolve.
import type { FlowDefinition, FlowTask } from "@/components/graphs/types";

/**
 * One save session: a Graph, its queue, and whether a drain is running.
 *
 * The session is an object rather than a set of module-level refs so switching
 * Graphs replaces it wholesale. A drain from the previous Graph keeps writing to
 * the object it captured and stops at every `session.current !== target` check,
 * instead of mutating the new Graph's queue counters.
 */
export type FlushSession = {
  graphId: string;
  ops: QueuedOp[];
  /** ops[0, sent) have been accepted by the Worker. */
  sent: number;
  running: boolean;
};

/**
 * A save conflict put to the reader instead of resolved for them: the disk moved
 * under an unsent queue (conflict), or the Graph they are re-opening has edits
 * that a reload would adopt the disk definition over (reload).
 */
export type GraphConflict = { graphId: string; reason: "conflict" | "reload" };

/**
 * A Graph-local Hero draft.
 *
 * The contract's own Hero document type arrives with the local-Hero endpoints
 * (slice S3a). Until then the editor only carries the document through
 * untouched, so an opaque record is enough — inventing the shape here would
 * create a second definition to keep in sync with the Worker's.
 */
export type HeroDocument = Record<string, unknown>;

/** One unsent change to a Graph draft. */
export type QueuedOp =
  | { graphId: string; kind: "name"; value: string }
  | { graphId: string; kind: "description"; value: string }
  | {
      graphId: string;
      kind: "task";
      id: string;
      /** null deletes the task, matching the PATCH body the Worker accepts. */
      value: FlowTask | null;
      /**
       * Set only when this op is the one that creates the Task's graph-local
       * Hero. It rides on the op because the Hero document is not part of the
       * FlowDefinition, so nothing else in the queue could carry it.
       */
      localHero?: HeroDocument;
    };

/**
 * applyOps replays unsent ops onto a definition to produce what the user sees.
 *
 * Pure and reference-preserving where it can be: no ops, or ops that change
 * nothing, return the input object unchanged, so the editor does not re-render
 * on a no-op. A task op always produces a new definition — comparing two tasks
 * deeply on every keystroke would cost more than the render it saves.
 */
export function applyOps(definition: FlowDefinition, ops: readonly QueuedOp[]): FlowDefinition {
  let next = definition;
  for (const op of ops) {
    switch (op.kind) {
      case "name":
        if (next.name === op.value) break;
        next = { ...next, name: op.value };
        break;
      case "description":
        if ((next.description ?? "") === op.value) break;
        next = { ...next, description: op.value };
        break;
      case "task": {
        if (op.value === null) {
          if (!(op.id in next.tasks)) break;
          // Deleting a task also drops it from every other task's depends, which
          // is what the Worker does to the file on the same mutation. Doing it
          // here keeps the screen equal to what the next GET would return.
          const tasks: Record<string, FlowTask> = {};
          for (const [id, task] of Object.entries(next.tasks)) {
            if (id === op.id) continue;
            const depends = task.depends ?? [];
            tasks[id] = depends.includes(op.id)
              ? { ...task, depends: depends.filter((dependency) => dependency !== op.id) }
              : task;
          }
          next = { ...next, tasks };
          break;
        }
        next = { ...next, tasks: { ...next.tasks, [op.id]: op.value } };
        break;
      }
    }
  }
  return next;
}

/** opToBody turns one op into the body of a PATCH /v1/graphs/{graphID}. */
export function opToBody(op: QueuedOp): Record<string, unknown> {
  switch (op.kind) {
    case "name":
      return { name: op.value };
    case "description":
      return { description: op.value };
    case "task": {
      const body: Record<string, unknown> = { task: { id: op.id, value: op.value } };
      // A delete never carries the Hero payload: there is nothing to create, and
      // the Worker refuses {task: {value: null}, localHero} outright.
      if (op.value !== null && op.localHero) body.localHero = op.localHero;
      return body;
    }
  }
}

/** sameTarget reports whether two ops write the same field of the same Graph. */
export function sameTarget(a: QueuedOp, b: QueuedOp): boolean {
  if (a.kind === "task" && b.kind === "task") return a.id === b.id;
  if (a.kind === "name" && b.kind === "name") return true;
  if (a.kind === "description" && b.kind === "description") return true;
  return false;
}

/**
 * mergeQueuedOp folds a new op over the unsent one it replaces.
 *
 * The Hero creation intent belongs to the Task's birth, not to the fields edited
 * afterwards: without this, editing a Task's goal while its "Add Task (new local
 * Hero)" op is still queued would drop the Hero payload, and the Task would keep
 * a local/<id> reference nothing ever creates.
 *
 * A delete is the one case that must NOT inherit: the Worker rejects
 * {task: {value: null}, localHero}, so carrying it would turn a legitimate
 * delete into a 422.
 */
export function mergeQueuedOp(prior: QueuedOp | undefined, next: QueuedOp): QueuedOp {
  if (next.kind !== "task" || next.value === null) return next;
  if (prior?.kind !== "task") return next;
  return { ...next, localHero: next.localHero ?? prior.localHero };
}

/**
 * diffTaskIDs names the Tasks whose stored value differs between two definitions.
 *
 * It backs the conflict bar's "what actually changed" line. Comparing serialised
 * tasks rather than merging them is deliberate: the reader is being asked to choose
 * a whole version, so the UI only has to say which Tasks disagree, not how.
 */
export function diffTaskIDs(mine: FlowDefinition | null, disk: FlowDefinition | null): string[] {
  const mineTasks = mine?.tasks ?? {};
  const diskTasks = disk?.tasks ?? {};
  const ids = new Set([...Object.keys(mineTasks), ...Object.keys(diskTasks)]);
  return [...ids]
    .filter((id) => JSON.stringify(mineTasks[id]) !== JSON.stringify(diskTasks[id]))
    .sort();
}

/**
 * removalNotice is the sentence the confirm dialog shows before a Task is deleted.
 *
 * It names the Tasks whose depends would be pruned, because the Worker removes
 * those edges on the same mutation and the reader should not discover it after the
 * fact. Kept pure so the phrasing is testable without mounting the dialog.
 */
export function removalNotice(definition: FlowDefinition | null, taskID: string): string {
  const downstream = Object.entries(definition?.tasks ?? {})
    .filter(([, task]) => (task.depends ?? []).includes(taskID))
    .map(([id]) => id);
  if (downstream.length === 0) {
    return "This removes the task from the graph draft. Published versions are unaffected.";
  }
  const listed = downstream.slice(0, 3).map((id) => `“${id}”`).join(", ")
    + (downstream.length > 3 ? ` and ${downstream.length - 3} more` : "");
  return `Removing “${taskID}” also removes it from the dependencies of ${listed}. Published versions are unaffected.`;
}
