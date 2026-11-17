"use client";

import { Check, Loader2, Plus, Workflow } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { GraphBuilderPanel } from "@/components/graphs/GraphBuilderPanel";
import { GraphSaveBanners } from "@/components/graphs/GraphSaveBanners";
import { confirmAction } from "@/components/ui/confirm";
import { GraphDependencies } from "@/components/graphs/GraphDependencies";
import { GraphTabs } from "@/components/graphs/GraphTabs";
import { TaskAccordion } from "@/components/graphs/TaskAccordion";
import {
  modelOptions,
  type FlowDefinition,
  type FlowTask,
  type GraphDetail,
  type GraphSummary,
  type ImmutableFileRevision,
} from "@/components/graphs/types";
import { applyOps, mergeQueuedOp, opToBody, sameTarget, type QueuedOp } from "@/lib/graph-ops";
import { useSettingsProfiles } from "@/hooks/useSettingsProfiles";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";
import { apiFetch, isSourceDigestConflict } from "@/lib/worker-api.client";

// A typing burst becomes one PATCH. The queue, not a timer, is the source of
// truth: the delay only decides when the drain starts, so an edit is never lost
// by a slow save, a fast keystroke, or a dialog closing.
const flushDelayMs = 300;

/**
 * One save session: a Graph, its queue, and whether a drain is running.
 *
 * The session is an object rather than a set of module-level refs so switching
 * Graphs replaces it wholesale. A drain from the previous Graph keeps writing to
 * the object it captured and stops at every `session.current !== target` check,
 * instead of mutating the new Graph's queue counters.
 */
type FlushSession = {
  graphId: string;
  ops: QueuedOp[];
  /** ops[0, sent) have been accepted by the Worker. */
  sent: number;
  running: boolean;
};

type Conflict = { graphId: string; reason: "conflict" | "reload" };

export function GraphSettings({ setError }: { setError: (value: string | null) => void }) {
  const settings = useSettingsProfiles();
  const models = useMemo(() => modelOptions(settings.models), [settings.models]);
  const [graphs, setGraphs] = useState<GraphSummary[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<GraphDetail | null>(null);
  // What the user sees: the last definition the Worker confirmed, with the ops
  // that have not been sent replayed on top. There is no third source.
  const [draft, setDraft] = useState<FlowDefinition | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [busy, setBusy] = useState<"loading" | "creating" | "publishing" | null>(null);
  const [saving, setSaving] = useState(false);
  const [pendingOps, setPendingOps] = useState(0);
  const [saveError, setSaveError] = useState<{ message: string; graphId: string } | null>(null);
  const [conflict, setConflict] = useState<Conflict | null>(null);
  const [diskPreview, setDiskPreview] = useState<{ graphId: string; name: string; changed: string[] } | null>(null);
  const [latestVersion, setLatestVersion] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);

  useOverlayLayer(createOpen, () => setCreateOpen(false));
  const [newGraph, setNewGraph] = useState({ id: "", name: "" });
  const [addTaskOpen, setAddTaskOpen] = useState(false);
  const [newTaskId, setNewTaskId] = useState("");
  const [mobileView, setMobileView] = useState<"tasks" | "graph" | "build">("tasks");
  // Pairs with the sequence number in openGraph: a tab switched quickly answers
  // out of order, and only the newest read may write the panel.
  const openRequestId = useRef(0);
  const session = useRef<FlushSession | null>(null);
  const serverDef = useRef<FlowDefinition | null>(null);
  const latestDraft = useRef<FlowDefinition | null>(null);
  const digest = useRef<string | null>(null);
  const flushTimer = useRef<number | null>(null);
  const flushInFlight = useRef<Promise<boolean> | null>(null);

  const refreshCatalog = useCallback(async (preferred?: string) => {
    const next = await apiFetch<GraphSummary[]>("/v1/graphs");
    setGraphs(next);
    const target = preferred ?? next.find((graph) => !graph.error)?.id ?? null;
    setSelected(target);
    return target;
  }, []);

  const openGraph = useCallback(async (id: string) => {
    const current = session.current;
    const reloading = current?.graphId === id;
    const unsent = reloading ? current.ops.length - current.sent : 0;

    // A reload of the Graph already open (Builder apply, or a manual revisit) must
    // not adopt the disk definition over unsent edits — that is the same silent
    // loss the queue exists to prevent. Offer the choice instead.
    if (reloading && (unsent > 0 || current.running)) {
      setConflict({ graphId: id, reason: "reload" });
      return;
    }

    const requestId = ++openRequestId.current;
    setBusy("loading");
    setSelected(id);
    setExpanded(null);
    try {
      const [next, versions] = await Promise.all([
        apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(id)}`),
        apiFetch<ImmutableFileRevision[]>(`/v1/graphs/${encodeURIComponent(id)}/versions`),
      ]);
      if (openRequestId.current !== requestId) return;
      if (!reloading) {
        session.current = { graphId: id, ops: [], sent: 0, running: false };
      }
      serverDef.current = next.definition ?? null;
      latestDraft.current = next.definition ?? null;
      digest.current = next.digest;
      setDetail(next);
      setDraft(next.definition ?? null);
      setPendingOps(0);
      setLatestVersion(versions.reduce((latest, version) => Math.max(latest, version.version), 0));
      setConflict(null);
      setSaveError(null);
      setDiskPreview(null);
      setError(null);
    } catch (reason) {
      if (openRequestId.current !== requestId) return;
      setDetail(null);
      setDraft(null);
      setError((reason as Error).message);
    } finally {
      if (openRequestId.current === requestId) setBusy(null);
    }
  }, [setError]);

  useEffect(() => {
    let cancelled = false;
    const timer = window.setTimeout(() => {
      void refreshCatalog().then((id) => {
        if (!cancelled && id) void openGraph(id);
      }).catch((reason: unknown) => {
        if (!cancelled) setError((reason as Error).message);
      });
    }, 0);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [openGraph, refreshCatalog, setError]);

  const adoptServerDefinition = (updated: GraphDetail) => {
    const definition = updated.definition ?? null;
    serverDef.current = definition;
    latestDraft.current = definition;
    digest.current = updated.digest;
    setDraft(definition);
    setGraphs((current) => current.map((graph) =>
      graph.id === updated.id ? { ...graph, name: updated.name, digest: updated.digest, error: undefined } : graph));
  };

  /**
   * drainQueue sends the unsent ops one PATCH at a time.
   *
   * Every success makes the Worker's answer the new base — including the values it
   * normalised — and replays the remaining ops on top, which is what keeps the
   * "unsaved" marker honest. Every failure stops the loop: there is no retry
   * storm, and recovery is an explicit action (the retry button, or the conflict
   * bar). Returns true when the queue emptied.
   */
  const drainQueue = useCallback(async (target: FlushSession): Promise<boolean> => {
    try {
      while (target.sent < target.ops.length) {
        const op = target.ops[target.sent];
        // Count the op as dispatched BEFORE awaiting it. `sent` is the boundary
        // between "settled" and "still the editor's to change", and an edit made
        // during the round trip must append a new op rather than coalesce into the
        // one already on the wire.
        target.sent += 1;
        let updated: GraphDetail;
        try {
          updated = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(target.graphId)}`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ expectedDigest: digest.current, ...opToBody(op) }),
          });
        } catch (reason) {
          // The op never landed, so it goes back to unsent and stays queued; the
          // reader decides when to retry rather than the loop retrying itself.
          target.sent -= 1;
          throw reason;
        }
        // The reader may have switched Graphs while this was in flight; their
        // answer is not ours to render.
        if (session.current !== target) return false;
        const base = updated.definition ?? serverDef.current!;
        serverDef.current = base;
        digest.current = updated.digest;
        latestDraft.current = applyOps(base, target.ops.slice(target.sent));
        setDraft(latestDraft.current);
        setPendingOps(target.ops.length - target.sent);
        setGraphs((current) => current.map((graph) =>
          graph.id === updated.id ? { ...graph, name: updated.name, digest: updated.digest, error: undefined } : graph));
        setConflict(null);
        setSaveError(null);
        setError(null);
      }
      // Keep the unsent tail only: the prefix is settled, and clearing wholesale
      // would drop an op that arrived while the last PATCH was in flight.
      target.ops = target.ops.slice(target.sent);
      target.sent = 0;
      setPendingOps(0);
      return true;
    } catch (reason) {
      if (session.current !== target) return false;
      setPendingOps(target.ops.length - target.sent);
      if (isSourceDigestConflict(reason)) setConflict({ graphId: target.graphId, reason: "conflict" });
      else setSaveError({ message: (reason as Error).message, graphId: target.graphId });
      return false;
    }
  }, [setError]);

  const flush = useCallback((target: FlushSession): Promise<boolean> => {
    if (session.current !== target) return Promise.resolve(false);
    if (target.running) return flushInFlight.current ?? Promise.resolve(false);
    target.running = true;
    setSaving(true);
    setSaveError(null);
    const running = drainQueue(target).finally(() => {
      target.running = false;
      flushInFlight.current = null;
      if (session.current === target) setSaving(false);
    });
    flushInFlight.current = running;
    return running;
  }, [drainQueue]);

  const scheduleFlush = useCallback((target: FlushSession) => {
    if (flushTimer.current !== null) window.clearTimeout(flushTimer.current);
    flushTimer.current = window.setTimeout(() => {
      flushTimer.current = null;
      void flush(target);
    }, flushDelayMs);
  }, [flush]);

  /** enqueue is the editor's only write path. */
  const enqueue = useCallback((op: QueuedOp) => {
    const target = session.current;
    const base = serverDef.current;
    if (!target || !base || target.graphId !== op.graphId) return;

    const pending = target.ops.slice(target.sent);
    const prior = pending.find((candidate) => sameTarget(candidate, op));
    const kept = pending.filter((candidate) => !sameTarget(candidate, op));
    target.ops = [...target.ops.slice(0, target.sent), ...kept, mergeQueuedOp(prior, op)];

    latestDraft.current = applyOps(base, target.ops.slice(target.sent));
    setDraft(latestDraft.current);
    setPendingOps(target.ops.length - target.sent);
    scheduleFlush(target);
  }, [scheduleFlush]);

  // A dialog can be closed mid-typing. The queue holds the edit, so kick the
  // drain before the debounce timer dies with the component; the request finishes
  // even though the state setters it would use are gone.
  const drainOnUnmount = useRef<(target: FlushSession) => void>(() => {});
  useEffect(() => {
    drainOnUnmount.current = (target) => { void flush(target); };
  }, [flush]);
  useEffect(() => () => {
    if (flushTimer.current !== null) window.clearTimeout(flushTimer.current);
    const pending = session.current;
    if (pending && pending.sent < pending.ops.length && !pending.running) drainOnUnmount.current(pending);
  }, []);

  const createGraph = async () => {
    if (!newGraph.id.trim() || !newGraph.name.trim()) return;
    setBusy("creating");
    try {
      const created = await apiFetch<GraphDetail>("/v1/graphs", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: newGraph.id.trim(), name: newGraph.name.trim() }),
      });
      setCreateOpen(false);
      setNewGraph({ id: "", name: "" });
      await refreshCatalog(created.id);
      await openGraph(created.id);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const publishGraph = async () => {
    if (!draft || busy) return;
    const target = session.current;
    // Publishing freezes what the Worker has, so unsent edits must land first —
    // otherwise the version published is not the one on screen.
    if (target && target.sent < target.ops.length) {
      const drained = await flush(target);
      if (!drained) return;
    }
    if (conflict) return;
    setBusy("publishing");
    try {
      const version = await apiFetch<ImmutableFileRevision>(`/v1/graphs/${encodeURIComponent(draft.id)}/publish`, { method: "POST" });
      setLatestVersion(version.version);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const updateTask = (id: string, task: FlowTask) => {
    if (!detail) return;
    enqueue({ graphId: detail.id, kind: "task", id, value: task });
  };

  const removeTask = async (id: string) => {
    const definition = draft;
    const downstreamIds = Object.entries(definition?.tasks ?? {})
      .filter(([, task]) => (task.depends ?? []).includes(id))
      .map(([taskId]) => taskId);
    const listed = downstreamIds.slice(0, 3).map((taskId) => `“${taskId}”`).join(", ")
      + (downstreamIds.length > 3 ? ` and ${downstreamIds.length - 3} more` : "");
    const description = downstreamIds.length > 0
      ? `Removing “${id}” also removes it from the dependencies of ${listed}. Published versions are unaffected.`
      : "This removes the task from the graph draft. Published versions are unaffected.";
    if (!(await confirmAction({
      title: `Remove Task “${id}”?`,
      description,
      confirmLabel: "Remove task",
      tone: "danger",
    }))) return;
    if (!detail) return;
    enqueue({ graphId: detail.id, kind: "task", id, value: null });
  };

  const addTask = () => {
    const id = newTaskId.trim();
    if (!id || !detail) return;
    enqueue({ graphId: detail.id, kind: "task", id, value: { hero: `local/${id}`, goal: "Describe the outcome this Task must produce." } });
    setNewTaskId("");
    setAddTaskOpen(false);
    setExpanded(id);
  };

  const setTaskDependencies = (taskId: string, depends: string[]) => {
    const task = draft?.tasks[taskId];
    if (!detail || !task) return;
    // One op shape for every task edit: the Worker replaces the whole task, so a
    // separate `dependencies` mutation would send the same change twice.
    enqueue({ graphId: detail.id, kind: "task", id: taskId, value: { ...task, depends } });
  };

  /** Re-fetch the Graph the conflict is about and describe how it differs. */
  const viewDiskVersion = async () => {
    const target = session.current;
    if (!target) return;
    try {
      const disk = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(target.graphId)}`);
      const diskTasks = disk.definition?.tasks ?? {};
      const mineTasks = latestDraft.current?.tasks ?? {};
      const changed = [...new Set([...Object.keys(diskTasks), ...Object.keys(mineTasks)])]
        .filter((id) => JSON.stringify(diskTasks[id]) !== JSON.stringify(mineTasks[id]))
        .sort();
      setDiskPreview({ graphId: target.graphId, name: disk.definition?.name ?? disk.name, changed });
    } catch (reason) {
      setError((reason as Error).message);
    }
  };

  const keepMine = async () => {
    const target = session.current;
    if (!target || target.running) return;
    try {
      const disk = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(target.graphId)}`);
      // Rebase onto the disk definition so the next CAS carries its digest; my
      // queue is unchanged and goes out on top of it.
      const base = disk.definition ?? serverDef.current!;
      serverDef.current = base;
      digest.current = disk.digest;
      latestDraft.current = applyOps(base, target.ops.slice(target.sent));
      setDraft(latestDraft.current);
      setConflict(null);
      setDiskPreview(null);
      if (target.sent < target.ops.length) void flush(target);
    } catch (reason) {
      setError((reason as Error).message);
    }
  };

  const takeTheirs = async () => {
    const target = session.current;
    if (!target || target.running) return;
    try {
      const disk = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(target.graphId)}`);
      target.ops = [];
      target.sent = 0;
      adoptServerDefinition(disk);
      setPendingOps(0);
      setConflict(null);
      setSaveError(null);
      setDiskPreview(null);
    } catch (reason) {
      setError((reason as Error).message);
    }
  };

  const definition = draft;
  // Compare against the rendered Graph, not the session ref: a ref read during
  // render is invisible to React's scheduler and would not re-run on change.
  const activeGraphId = detail?.id ?? null;
  const conflictForThisGraph = conflict && conflict.graphId === activeGraphId ? conflict : null;
  const saveErrorForThisGraph = saveError && saveError.graphId === activeGraphId ? saveError : null;
  const saveState = saving ? "saving" : pendingOps > 0 ? "unsaved" : "saved";

  return (
    <div className="graph-workspace">
      <GraphTabs graphs={graphs} selected={selected} onSelect={(id) => void openGraph(id)} onAdd={() => setCreateOpen(true)} />

      {createOpen && (
        <div className="graph-create-strip" role="dialog" aria-label="Add Graph">
          <label>ID<input autoFocus value={newGraph.id} onChange={(event) => setNewGraph((value) => ({ ...value, id: event.target.value }))} placeholder="rna-seq" /></label>
          <label>Name<input value={newGraph.name} onChange={(event) => setNewGraph((value) => ({ ...value, name: event.target.value }))} placeholder="RNA-seq" /></label>
          <button type="button" onClick={() => void createGraph()} disabled={busy === "creating"}>{busy === "creating" ? "Creating…" : "Create Graph"}</button>
          <button type="button" onClick={() => setCreateOpen(false)}>Cancel</button>
        </div>
      )}

      <div className="graph-mobile-segments" role="tablist" aria-label="Graph views">
        {(["tasks", "graph", "build"] as const).map((view) => (
          <button key={view} type="button" role="tab" aria-selected={mobileView === view} onClick={() => setMobileView(view)}>{view}</button>
        ))}
      </div>

      {!detail && busy === "loading" && <div className="graph-loading"><Loader2 className="spin" size={18} /> Loading Graph…</div>}
      {!detail && busy !== "loading" && graphs.length === 0 && (
        <div className="graph-welcome"><Workflow size={28} /><h2>Create your first Graph</h2><p>Compose reusable Tasks and their dependencies without choosing a Project.</p><button type="button" onClick={() => setCreateOpen(true)}><Plus size={16} /> Add Graph</button></div>
      )}

      {detail && definition && (
        <div className="graph-document">
          <header className="graph-document-header">
            <div><span>Graph</span><h2>{definition.name ?? definition.id}</h2><code>{definition.id}</code></div>
            <div className="graph-document-actions">
              {latestVersion > 0 && <span>v{latestVersion}</span>}
              <div className={`graph-save-state is-${saveState}`} role="status">{saveState === "saving"
                ? <><Loader2 className="spin" size={14} /> Saving</>
                : saveState === "unsaved"
                  ? <>Unsaved changes</>
                  : <><Check size={14} /> Draft saved</>}</div>
              <button type="button" onClick={() => void publishGraph()} disabled={Boolean(busy) || saving || Boolean(conflictForThisGraph) || Object.keys(definition.tasks).length === 0}>{busy === "publishing" ? "Publishing…" : "Publish"}</button>
            </div>
          </header>

          <GraphSaveBanners
            saveError={saveErrorForThisGraph}
            conflict={conflictForThisGraph}
            saving={saving}
            diskPreview={diskPreview ? { name: diskPreview.name, changed: diskPreview.changed } : null}
            onRetry={() => { const target = session.current; if (target) void flush(target); }}
            onViewDisk={() => void viewDiskVersion()}
            onKeepMine={() => void keepMine()}
            onDiscardMine={() => void takeTheirs()}
          />

          <main className={`graph-editor-view is-${mobileView}`}>
            <section className="graph-tasks-pane">
              <div className="graph-section-heading"><div><span>Tasks</span><p>Execution configuration. Tasks are collapsed by default.</p></div><button type="button" onClick={() => setAddTaskOpen((value) => !value)}><Plus size={16} /> Add Task</button></div>
              {addTaskOpen && <div className="graph-add-task"><input value={newTaskId} onChange={(event) => setNewTaskId(event.target.value)} placeholder="task_id" /><button type="button" onClick={addTask}>Add</button></div>}
              <div className="graph-task-list">
                {Object.keys(definition.tasks).sort().map((id) => (
                  <TaskAccordion key={id} id={id} task={definition.tasks[id]} expanded={expanded === id} onToggle={() => setExpanded((value) => value === id ? null : id)} onChange={(task) => updateTask(id, task)} onRemove={() => void removeTask(id)} />
                ))}
                {Object.keys(definition.tasks).length === 0 && <div className="graph-empty-state">No Tasks yet. Add one or ask Builder to draft the Graph.</div>}
              </div>
            </section>

            <section className="graph-dependencies-pane">
              <div className="graph-section-heading"><div><span>Graph</span><p>Dependency orchestration. Hero details stay inside Tasks.</p></div></div>
              <GraphDependencies definition={definition} busy={Boolean(busy)} onOpenTask={(id) => { setExpanded(id); setMobileView("tasks"); requestAnimationFrame(() => document.querySelector(`[data-task-id="${CSS.escape(id)}"]`)?.scrollIntoView({ behavior: "smooth", block: "start" })); }} onChange={(taskId, depends) => setTaskDependencies(taskId, depends)} />
            </section>

            <GraphBuilderPanel
              graphId={detail.id}
              models={models}
              setError={setError}
              onApplied={(id) => void openGraph(id)}
            />
          </main>
        </div>
      )}
    </div>
  );
}
