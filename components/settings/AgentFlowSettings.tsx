"use client";

import { Check, Loader2, Plus, Workflow } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { GraphBuilderPanel } from "@/components/graphs/GraphBuilderPanel";
import { GraphDependencies } from "@/components/graphs/GraphDependencies";
import { GraphTabs } from "@/components/graphs/GraphTabs";
import { TaskAccordion } from "@/components/graphs/TaskAccordion";
import { modelOptions, type GraphDetail, type GraphSummary, type GraphTask } from "@/components/graphs/types";
import { useSettingsProfiles } from "@/hooks/useSettingsProfiles";
import { apiFetch } from "@/lib/worker-api.client";

export function AgentFlowSettings({ setError }: { setError: (value: string | null) => void }) {
  const settings = useSettingsProfiles();
  const models = useMemo(() => modelOptions(settings.models, settings.providers), [settings.models, settings.providers]);
  const [graphs, setGraphs] = useState<GraphSummary[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<GraphDetail | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [busy, setBusy] = useState<"loading" | "saving" | "creating" | "publishing" | null>(null);
  const [latestVersion, setLatestVersion] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [newGraph, setNewGraph] = useState({ id: "", name: "" });
  const [addTaskOpen, setAddTaskOpen] = useState(false);
  const [newTask, setNewTask] = useState({ id: "", name: "" });
  const [mobileView, setMobileView] = useState<"tasks" | "graph" | "build">("tasks");

  const refreshCatalog = useCallback(async (preferred?: string) => {
    const next = await apiFetch<GraphSummary[]>("/v1/graphs");
    setGraphs(next);
    const target = preferred ?? next.find((graph) => !graph.error)?.id ?? null;
    setSelected(target);
    return target;
  }, []);

  const openGraph = useCallback(async (id: string) => {
    setBusy("loading");
    setSelected(id);
    setExpanded(null);
    try {
      const [next, versions] = await Promise.all([
        apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(id)}`),
        apiFetch<Array<{ version: number }>>(`/v1/graphs/${encodeURIComponent(id)}/versions`),
      ]);
      setDetail(next);
      setLatestVersion(versions.reduce((latest, version) => Math.max(latest, version.version), 0));
      setError(null);
    } catch (reason) {
      setDetail(null);
      setError((reason as Error).message);
    } finally {
      setBusy(null);
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

  const createGraph = async () => {
    if (!newGraph.id.trim() || !newGraph.name.trim()) return;
    setBusy("creating");
    try {
      const created = await apiFetch<GraphDetail>("/v1/graphs", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: newGraph.id.trim(), name: newGraph.name.trim() }),
      });
      setCreateOpen(false);
      setNewGraph({ id: "", name: "" });
      setDetail(created);
      setLatestVersion(0);
      setSelected(created.id);
      await refreshCatalog(created.id);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const patchGraph = async (patch: Record<string, unknown>) => {
    if (!detail || busy) return;
    setBusy("saving");
    try {
      const updated = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(detail.id)}`, {
        method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expectedDigest: detail.digest, ...patch }),
      });
      setDetail(updated);
      setGraphs((current) => current.map((graph) => graph.id === updated.id ? { ...graph, name: updated.name, digest: updated.digest, error: undefined } : graph));
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const publishGraph = async () => {
    if (!detail || busy) return;
    setBusy("publishing");
    try {
      const version = await apiFetch<{ version: number }>(`/v1/graphs/${encodeURIComponent(detail.id)}/publish`, { method: "POST" });
      setLatestVersion(version.version);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const saveTask = (id: string, task: GraphTask) => patchGraph({ task: { id, value: task } });
  const removeTask = (id: string) => {
    if (!window.confirm(`Remove Task “${detail?.document.tasks[id]?.name ?? id}”?`)) return;
    void patchGraph({ task: { id, value: null } });
  };
  const addTask = async () => {
    const model = models[0];
    if (!model) {
      setError("Create an active model before adding an inline Task.");
      return;
    }
    const id = newTask.id.trim();
    const name = newTask.name.trim();
    if (!id || !name) return;
    await patchGraph({ task: { id, value: { name, model: model.ref, thinking: "default", skills: [], goal: "Describe the outcome this Task must produce." } } });
    setNewTask({ id: "", name: "" });
    setAddTaskOpen(false);
    setExpanded(id);
  };

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

      {detail && (
        <div className="graph-document">
          <header className="graph-document-header">
            <div><span>Graph</span><h2>{detail.document.name}</h2><code>{detail.document.id}</code></div>
            <div className="graph-document-actions">
              <span>v{latestVersion || "draft"}</span>
              <div className={`graph-save-state ${busy === "saving" ? "is-saving" : ""}`}>{busy === "saving" ? <><Loader2 className="spin" size={14} /> Saving</> : <><Check size={14} /> Saved</>}</div>
              <button type="button" onClick={() => void publishGraph()} disabled={Boolean(busy) || Object.keys(detail.document.tasks).length === 0}>{busy === "publishing" ? "Publishing…" : "Publish"}</button>
            </div>
          </header>

          <main className={`graph-editor-view is-${mobileView}`}>
            <section className="graph-tasks-pane">
              <div className="graph-section-heading"><div><span>Tasks</span><p>Execution configuration. Tasks are collapsed by default.</p></div><button type="button" onClick={() => setAddTaskOpen((value) => !value)}><Plus size={16} /> Add Task</button></div>
              {addTaskOpen && <div className="graph-add-task"><input value={newTask.id} onChange={(event) => setNewTask((value) => ({ ...value, id: event.target.value }))} placeholder="task_id" /><input value={newTask.name} onChange={(event) => setNewTask((value) => ({ ...value, name: event.target.value }))} placeholder="Task name" /><button type="button" onClick={() => void addTask()}>Add</button></div>}
              <div className="graph-task-list">
                {Object.keys(detail.document.tasks).sort().map((id) => (
                  <TaskAccordion key={id} id={id} task={detail.document.tasks[id]} expanded={expanded === id} busy={Boolean(busy)} models={models} onToggle={() => setExpanded((value) => value === id ? null : id)} onSave={(task) => void saveTask(id, task)} onRemove={() => removeTask(id)} />
                ))}
                {Object.keys(detail.document.tasks).length === 0 && <div className="graph-empty-state">No Tasks yet. Add one or ask Builder to draft the Graph.</div>}
              </div>
            </section>

            <section className="graph-dependencies-pane">
              <div className="graph-section-heading"><div><span>Graph</span><p>Dependency orchestration. Role details stay inside Tasks.</p></div></div>
              <GraphDependencies document={detail.document} busy={Boolean(busy)} onOpenTask={(id) => { setExpanded(id); setMobileView("tasks"); requestAnimationFrame(() => document.querySelector(`[data-task-id="${CSS.escape(id)}"]`)?.scrollIntoView({ behavior: "smooth", block: "start" })); }} onChange={(taskId, depends) => void patchGraph({ dependencies: { taskId, depends } })} />
            </section>

            <GraphBuilderPanel
              graphId={detail.id}
              models={models}
              setError={setError}
              onApplied={(updated) => {
                setDetail(updated);
                setGraphs((current) => current.map((graph) => graph.id === updated.id ? { ...graph, name: updated.name, digest: updated.digest, error: undefined } : graph));
              }}
            />
          </main>
        </div>
      )}
    </div>
  );
}
