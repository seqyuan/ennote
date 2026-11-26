"use client";

import { Check, Plus, Workflow } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { GraphBuilderPanel } from "@/components/graphs/GraphBuilderPanel";
import { AddTaskForm } from "@/components/graphs/AddTaskForm";
import { GraphHeader } from "@/components/graphs/GraphHeader";
import { GraphSaveBanners } from "@/components/graphs/GraphSaveBanners";
import { TaskList } from "@/components/graphs/TaskList";
import { confirmAction } from "@/components/ui/confirm";
import { SettingsLoading } from "@/components/settings/SettingsPrimitives";
import { GraphDependencies } from "@/components/graphs/GraphDependencies";
import { GraphTabs } from "@/components/graphs/GraphTabs";
import {
  modelOptions,
  type FlowDefinition,
  type FlowTask,
  type GraphDetail,
  type GraphSummary,
  type ImmutableFileRevision,
} from "@/components/graphs/types";
import { applyOps, mergeQueuedOp, opToBody, removalNotice, sameTarget, type FlushSession, type GraphConflict, type QueuedOp } from "@/lib/graph-ops";
import { useDiskConflictChoice } from "@/hooks/useDiskConflictChoice";
import { summariseGraphIdentity, type PublishedRevision } from "@/lib/graph-identity";
import type { components } from "@/lib/worker-api.gen";
import { useSettingsProfiles } from "@/hooks/useSettingsProfiles";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";
import { apiFetch, isSourceDigestConflict } from "@/lib/worker-api.client";

// A typing burst becomes one PATCH. The queue, not a timer, is the source of
// truth: the delay only decides when the drain starts, so an edit is never lost
// by a slow save, a fast keystroke, or a dialog closing.
const flushDelayMs = 300;


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
  // The Graph whose drain is in flight, not a global flag: a save that belongs to
  // the Graph the reader switched away from must not report the new Graph as
  // saving, and must not disable its Publish.
  const [savingGraph, setSavingGraph] = useState<string | null>(null);
  const [pendingOps, setPendingOps] = useState(0);
  const [saveError, setSaveError] = useState<{ message: string; graphId: string } | null>(null);
  const [conflict, setConflict] = useState<GraphConflict | null>(null);
  // Tagged with its Graph for the same reason: a publish that finishes after the
  // reader switched Graphs is that Graph's revision, not this one's.
  const [publishedFor, setPublishedFor] = useState<{ graphId: string; revision: PublishedRevision;
    heroDigests?: Record<string, string> } | null>(null);
  // The draft's Hero digests, so the header can tell "published" from "published
  // before I edited a Hero": the definition digest cannot see a Hero's body.
  const [draftHeroDigests, setDraftHeroDigests] = useState<Record<string, string> | null>(null);
  const [bindings, setBindings] = useState<components["schemas"]["GraphBindingRef"][]>([]);
  const [draftDigest, setDraftDigest] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  useOverlayLayer(createOpen, () => setCreateOpen(false));
  const [newGraph, setNewGraph] = useState({ id: "", name: "" });
  const [addTaskOpen, setAddTaskOpen] = useState(false);
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
  // openGraph has to drain the Graph it is leaving, and flush is declared below
  // it: the reader stays the caller and reaches the drainer through this ref.
  const flushPending = useRef<(target: FlushSession) => Promise<boolean>>(async () => true);

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
    // Switching away from another Graph replaces the session object, so an unsent
    // edit or a drain in flight would go with it. Land it first, and stay put if
    // it cannot land: the reader gets the save error instead of a silent loss.
    if (!reloading && current && (current.ops.length > current.sent || current.running)) {
      if (!(await flushPending.current(current))) return;
    }

    const requestId = ++openRequestId.current;
    setBusy("loading");
    setSelected(id);
    setExpanded(null);
    try {
      const [next, versions, boundTo] = await Promise.all([
        apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(id)}`),
        apiFetch<ImmutableFileRevision[]>(`/v1/graphs/${encodeURIComponent(id)}/versions`),
        // A failed bindings read must not blank the editor: it degrades the header
        // to "not bound", which is wrong but recoverable, unlike losing the draft.
        apiFetch<components["schemas"]["GraphBindingRef"][]>(`/v1/graphs/${encodeURIComponent(id)}/bindings`)
          .catch(() => [] as components["schemas"]["GraphBindingRef"][]),
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
      setDraftDigest(next.digest);
      setPendingOps(0);
      const newest = versions.reduce<ImmutableFileRevision | null>(
        (latest, version) => latest === null || version.version > latest.version ? version : latest, null);
      setPublishedFor({ graphId: id, heroDigests: newest?.heroDigests,
        revision: newest === null ? null
          : { version: newest.version, digest: newest.digest, publishedAt: newest.publishedAt } });
      setDraftHeroDigests(next.heroDigests ?? {});
      setBindings(boundTo);
      setConflict(null);
      setSaveError(null);
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
        setDraftDigest(updated.digest);
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
    setSavingGraph(target.graphId);
    setSaveError(null);
    const running = drainQueue(target).finally(() => {
      target.running = false;
      flushInFlight.current = null;
      setSavingGraph((current) => (current === target.graphId ? null : current));
    });
    flushInFlight.current = running;
    return running;
  }, [drainQueue]);

  useEffect(() => { flushPending.current = flush; }, [flush]);

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
    const publishedID = draft.id;
    setBusy("publishing");
    try {
      const version = await apiFetch<ImmutableFileRevision>(`/v1/graphs/${encodeURIComponent(publishedID)}/publish`, { method: "POST" });
      // The reader may have switched Graphs while this was in flight; the revision
      // belongs to the Graph that was published, not the one on screen.
      if (session.current?.graphId === publishedID) {
        setPublishedFor({ graphId: publishedID,
          revision: { version: version.version, digest: version.digest, publishedAt: version.publishedAt } });
        setError(null);
      }
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      // Only clear the state this call set: a Graph opened meanwhile owns "loading".
      setBusy((current) => (current === "publishing" ? null : current));
    }
  };

  const updateTask = (id: string, task: FlowTask) => {
    if (!detail) return;
    enqueue({ graphId: detail.id, kind: "task", id, value: task });
  };

  const removeTask = async (id: string) => {
    if (!(await confirmAction({
      title: `Remove Task “${id}”?`,
      description: removalNotice(draft, id),
      confirmLabel: "Remove task",
      tone: "danger",
    }))) return;
    if (!detail) return;
    enqueue({ graphId: detail.id, kind: "task", id, value: null });
  };

  const addTask = (added: { id: string; value: FlowTask; localHero?: Record<string, unknown> }) => {
    if (!detail) return;
    enqueue({ graphId: detail.id, kind: "task", id: added.id, value: added.value, localHero: added.localHero });
    setAddTaskOpen(false);
    setExpanded(added.id);
  };

  const setTaskDependencies = (taskId: string, depends: string[]) => {
    const task = draft?.tasks[taskId];
    if (!detail || !task) return;
    // One op shape for every task edit: the Worker replaces the whole task, so a
    // separate `dependencies` mutation would send the same change twice.
    enqueue({ graphId: detail.id, kind: "task", id: taskId, value: { ...task, depends } });
  };

  // The disk side of a conflict is its own hook: read-then-write, guarded by the
  // session it started from.
  const { diskPreview, clearDiskPreview, viewDiskVersion, keepMine, discardMine } = useDiskConflictChoice({
    sessionRef: session, serverDefRef: serverDef, latestDraftRef: latestDraft, digestRef: digest,
    flush, adoptServerDefinition, setDraft, setPendingOps, setConflict, setSaveError, setError,
  });
  // The preview describes one Graph's disk version, so it cannot outlive the
  // Graph it was read for.
  const shownGraphID = detail?.id ?? null;
  useEffect(() => { clearDiskPreview(); }, [shownGraphID, clearDiskPreview]);

  // A Hero save does not change the definition digest, so the header has to re-read
  // the digests it compares; nothing else in the detail is adopted.
  const refreshDraftHeroDigests = useCallback(async () => {
    const graphID = session.current?.graphId;
    if (!graphID) return;
    try {
      const next = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(graphID)}`);
      if (session.current?.graphId !== graphID) return;
      setDraftHeroDigests(next.heroDigests ?? {});
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [setError]);

  const definition = draft;
  // Compare against the rendered Graph, not the session ref: a ref read during
  // render is invisible to React's scheduler and would not re-run on change.
  const activeGraphId = detail?.id ?? null;
  // The draft digest is the Graph detail's own digest, which is state: reading the
  // ref here would be a ref access during render.
  const publishedForThisGraph = publishedFor && publishedFor.graphId === activeGraphId
    ? publishedFor.revision : null;
  const identity = summariseGraphIdentity({
    draftDigest: draftDigest,
    published: publishedForThisGraph,
    bindings,
    draftHeroDigests,
    publishedHeroDigests: publishedFor && publishedFor.graphId === activeGraphId ? publishedFor.heroDigests : null,
  });
  const conflictForThisGraph = conflict && conflict.graphId === activeGraphId ? conflict : null;
  const saveErrorForThisGraph = saveError && saveError.graphId === activeGraphId ? saveError : null;
  const saving = savingGraph === activeGraphId;
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

      {!detail && busy === "loading" && <div className="graph-loading"><SettingsLoading label="Loading Graph…" /></div>}
      {!detail && busy !== "loading" && graphs.length === 0 && (
        <div className="graph-welcome"><Workflow size={28} /><h2>Create your first Graph</h2><p>Compose reusable Tasks and their dependencies without choosing a Project.</p><button type="button" onClick={() => setCreateOpen(true)}><Plus size={16} /> Add Graph</button></div>
      )}

      {detail && definition && (
        <div className="graph-document">
          <GraphHeader
            definition={definition}
            identity={identity}
            saveState={saveState}
            publishing={busy === "publishing"}
            publishDisabled={Boolean(busy) || saving || Boolean(conflictForThisGraph) || Object.keys(definition.tasks).length === 0}
            onPublish={() => void publishGraph()}
          />

          <GraphSaveBanners
            saveError={saveErrorForThisGraph}
            conflict={conflictForThisGraph}
            saving={saving}
            diskPreview={diskPreview ? { name: diskPreview.name, changed: diskPreview.changed } : null}
            onRetry={() => { const target = session.current; if (target) void flush(target); }}
            onViewDisk={() => void viewDiskVersion()}
            onKeepMine={() => void keepMine()}
            onDiscardMine={() => void discardMine()}
          />

          <main className={`graph-editor-view is-${mobileView}`}>
            <section className="graph-tasks-pane">
              <div className="graph-section-heading"><div><span>Tasks</span><p>Execution configuration. Tasks are collapsed by default.</p></div><button type="button" onClick={() => setAddTaskOpen((value) => !value)}><Plus size={16} /> Add Task</button></div>
              {addTaskOpen && (
                <AddTaskForm
                  takenIds={Object.keys(definition.tasks)}
                  defaultGoal="Describe the outcome this Task must produce."
                  modelRef={models[0]?.ref ?? ""}
                  onAdd={addTask}
                  onCancel={() => setAddTaskOpen(false)}
                />
              )}
              <TaskList
                graphId={detail.id}
                definition={definition}
                expanded={expanded}
                models={models}
                setError={setError}
                onHeroSaved={() => void refreshDraftHeroDigests()}
                onToggle={(id) => setExpanded((value) => value === id ? null : id)}
                onChange={updateTask}
                onRemove={(id) => void removeTask(id)}
              />
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
