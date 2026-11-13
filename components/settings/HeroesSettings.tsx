"use client";

import { ArrowLeft, Bot, Check, FileText, Loader2, Plus, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { modelOptions } from "@/components/graphs/types";
import { useT } from "@/components/LocaleProvider";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";
import type { ModelProfile } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

type ThinkingEffort = "default" | "low" | "medium" | "high";

// The frozen HeroContextPolicy vocabulary. task_only is required for a Hero to
// run as a delegated child; reply enables reply_to addressing.
const ROLE_CONTEXT_MODES = ["room", "reply", "fresh", "task_only"] as const;
type HeroContextMode = (typeof ROLE_CONTEXT_MODES)[number];
const ROLE_CONTEXT_LABELS: Record<HeroContextMode, string> = {
  room: "Room", reply: "Reply", fresh: "Fresh", task_only: "Task only",
};

// A Hero's permission ceiling is the same three modes a Run runs under, ordered
// by strictness so the tightest option is the first one read.
const PERMISSION_CEILINGS = ["discuss", "ask", "auto"] as const;

interface HeroDocument {
  schemaVersion: number;
  handle: string;
  name: string;
  description: string;
  positioning: string;
  icon: string;
  color: string;
  model: { ref: string; thinkingEffort: ThinkingEffort; fallbacks: string[] };
  skills: Array<{ id: string; mode: "preload" | "available" }>;
  authority: "read_only" | "write_workspace";
  permissionCeiling: "discuss" | "auto" | "ask";
  allowedTools: string[];
  context: { defaultMode: HeroContextMode; allowedModes: HeroContextMode[]; ownExecutionContinuity: "none" };
  delegation: {
    admission: "auto_within_budget" | "approval_required" | "deny";
    allowedCallerKinds: string[];
    allowedStrategies: string[];
    maxInvocationsPerParentRun: number;
    maxConcurrentInstances: number;
    budgetCeiling: { maxModelCalls: number; maxToolCalls: number; maxTotalTokens: number; maxOutputTokens: number; maxCostUsdMicros: number; maxWallTimeMs: number };
  };
  outputContract: string;
  maxLoopIterations: number;
  prompt: string;
}

interface HeroSummary { id: string; name: string; path: string; digest?: string; error?: string }
interface HeroDetail { id: string; name: string; path: string; digest: string; document: HeroDocument }

function defaultHero(handle: string, name: string, model: string): HeroDocument {
  return {
    schemaVersion: 1, handle, name, description: "", positioning: "", icon: "bot", color: "neutral",
    model: { ref: model, thinkingEffort: "default", fallbacks: [] }, skills: [],
    authority: "read_only", permissionCeiling: "discuss", allowedTools: ["read", "ls", "grep", "find"],
    context: { defaultMode: "room", allowedModes: ["room", "fresh", "task_only"], ownExecutionContinuity: "none" },
    delegation: {
      admission: "auto_within_budget", allowedCallerKinds: ["host"], allowedStrategies: ["single", "parallel"],
      maxInvocationsPerParentRun: 16, maxConcurrentInstances: 16,
      budgetCeiling: { maxModelCalls: 4, maxToolCalls: 8, maxTotalTokens: 20000, maxOutputTokens: 4000, maxCostUsdMicros: 100000, maxWallTimeMs: 120000 },
    },
    outputContract: "text-v1", maxLoopIterations: 8,
    prompt: "Perform the requested work independently. Distinguish evidence from assumptions and report a concise result.",
  };
}

export function HeroesSettings({ models, setError }: {
  models: ModelProfile[];
  setError: (value: string | null) => void;
}) {
  const t = useT();
  const options = useMemo(() => modelOptions(models), [models]);
  const [heroes, setHeroes] = useState<HeroSummary[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<HeroDetail | null>(null);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<"loading" | "saving" | "creating" | "publishing" | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [newHero, setNewHero] = useState({ id: "", name: "" });
  const [conflict, setConflict] = useState(false);
  const [diskDetail, setDiskDetail] = useState<HeroDetail | null>(null);

  useOverlayLayer(diskDetail !== null, () => setDiskDetail(null));

  const refresh = useCallback(async () => {
    const next = await apiFetch<HeroSummary[]>("/v1/global-heroes");
    setHeroes(next);
    return next;
  }, []);

  const openHero = useCallback(async (id: string) => {
    setBusy("loading");
    setSelected(id);
    try {
      setDetail(await apiFetch<HeroDetail>(`/v1/global-heroes/${encodeURIComponent(id)}`));
      setConflict(false);
      setDiskDetail(null);
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
      void refresh().then((items) => {
        const first = items.find((hero) => !hero.error);
        if (!cancelled && first) void openHero(first.id);
      }).catch((reason: unknown) => { if (!cancelled) setError((reason as Error).message); });
    }, 0);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [openHero, refresh, setError]);

  const createHero = async () => {
    if (!options[0]) { setError("Create an active model before creating a Hero."); return; }
    const id = newHero.id.trim();
    const name = newHero.name.trim();
    if (!id || !name) return;
    setBusy("creating");
    try {
      const created = await apiFetch<HeroDetail>("/v1/global-heroes", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ document: defaultHero(id, name, options[0].ref) }),
      });
      setCreateOpen(false);
      setNewHero({ id: "", name: "" });
      setDetail(created);
      setSelected(created.id);
      await refresh();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const save = async (document: HeroDocument) => {
    if (!detail || busy) return;
    setBusy("saving");
    try {
      const updated = await apiFetch<HeroDetail>(`/v1/global-heroes/${encodeURIComponent(detail.id)}`, {
        method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expectedDigest: detail.digest, document }),
      });
      setDetail(updated);
      setHeroes((current) => current.map((hero) => hero.id === updated.id ? { ...hero, name: updated.name, digest: updated.digest, error: undefined } : hero));
      setConflict(false);
      setDiskDetail(null);
      setError(null);
    } catch (reason) {
      setConflict(true);
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const showDiff = async () => {
    if (!detail) return;
    try {
      setDiskDetail(await apiFetch<HeroDetail>(`/v1/global-heroes/${encodeURIComponent(detail.id)}`));
    } catch (reason) {
      setError((reason as Error).message);
    }
  };

  const publish = async () => {
    if (!detail || busy) return;
    setBusy("publishing");
    try {
      await apiFetch(`/v1/global-heroes/${encodeURIComponent(detail.id)}/publish`, { method: "POST" });
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const updateLocal = (update: (document: HeroDocument) => HeroDocument) => {
    if (!detail) return;
    setDetail({ ...detail, document: update(detail.document) });
  };
  const updateAndSave = (update: (document: HeroDocument) => HeroDocument) => {
    if (!detail) return;
    const document = update(detail.document);
    setDetail({ ...detail, document });
    void save(document);
  };
  const toggleContextMode = (mode: HeroContextMode, checked: boolean) => {
    updateAndSave((document) => {
      const allowedModes = checked
        ? ROLE_CONTEXT_MODES.filter((candidate) => candidate === mode || document.context.allowedModes.includes(candidate))
        : document.context.allowedModes.filter((candidate) => candidate !== mode);
      if (allowedModes.length === 0) return document;
      const defaultMode = allowedModes.includes(document.context.defaultMode)
        ? document.context.defaultMode : allowedModes[0];
      return { ...document, context: { ...document.context, allowedModes, defaultMode } };
    });
  };

  const visible = heroes.filter((hero) => `${hero.name} ${hero.id}`.toLowerCase().includes(query.trim().toLowerCase()));

  return (
    <div className={`global-heroes ${selected ? "has-selection" : ""}`}>
      <aside className="global-heroes-list">
        <header><div><strong>Hero catalog</strong><span>{heroes.length} global Heroes</span></div><button type="button" onClick={() => setCreateOpen((value) => !value)} aria-label="Add Hero"><Plus size={18} /></button></header>
        {createOpen && <div className="global-hero-create"><input value={newHero.id} onChange={(event) => setNewHero((value) => ({ ...value, id: event.target.value }))} placeholder="hero-id" /><input value={newHero.name} onChange={(event) => setNewHero((value) => ({ ...value, name: event.target.value }))} placeholder="Hero name" /><button type="button" onClick={() => void createHero()} disabled={busy === "creating"}>{busy === "creating" ? "Creating…" : "Create"}</button></div>}
        <label className="global-hero-search"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search Heroes" /></label>
        <div role="listbox" aria-label="Global Heroes">
          {visible.map((hero) => <button key={hero.id} type="button" role="option" aria-selected={selected === hero.id} onClick={() => void openHero(hero.id)}><Bot size={17} /><span><strong>{hero.name}</strong><code>{hero.id}</code>{hero.error && <small>{hero.error}</small>}</span><FileText size={14} /></button>)}
          {visible.length === 0 && <div className="heroes-empty">No global Heroes</div>}
        </div>
      </aside>

      <section className="global-hero-detail">
        {!detail && busy === "loading" && <div className="heroes-detail-empty"><Loader2 className="spin" size={18} /> Loading Hero…</div>}
        {!detail && busy !== "loading" && <div className="heroes-detail-empty"><Bot size={26} /> Select or create a Hero</div>}
        {detail && <>
          <header className="global-hero-commandbar">
            <button type="button" className="global-hero-back" onClick={() => { setSelected(null); setDetail(null); }}><ArrowLeft size={17} /> Heroes</button>
            <div><strong>{detail.document.name}</strong><code>{detail.path}</code></div>
            <div className="global-hero-command-actions">
              {conflict ? <>
                <span className="hero-conflict-state">File changed on disk</span>
                <button type="button" onClick={() => void showDiff()}>Diff</button>
                <button type="button" onClick={() => void openHero(detail.id)}>Reload</button>
              </> : <span className={busy === "saving" ? "is-saving" : ""}>{busy === "saving" ? <><Loader2 className="spin" size={14} /> Saving</> : <><Check size={14} /> Saved</>}</span>}
              <button type="button" onClick={() => void publish()} disabled={Boolean(busy) || conflict}>{busy === "publishing" ? "Publishing…" : "Publish"}</button>
            </div>
          </header>
          <div className="global-hero-form">
            <section><h3>Identity</h3><div className="global-hero-grid">
              <label>Name<input value={detail.document.name} onChange={(event) => updateLocal((document) => ({ ...document, name: event.target.value }))} onBlur={() => void save(detail.document)} /></label>
              <label>Handle<input value={detail.document.handle} disabled /></label>
              <label className="wide">Description<input value={detail.document.description} onChange={(event) => updateLocal((document) => ({ ...document, description: event.target.value }))} onBlur={() => void save(detail.document)} /></label>
            </div></section>
            <section><h3>Runtime</h3><div className="global-hero-grid">
              <label className="wide">Model<select value={detail.document.model.ref} onChange={(event) => updateAndSave((document) => ({ ...document, model: { ...document.model, ref: event.target.value } }))}>{options.map((option) => <option key={option.ref} value={option.ref}>{option.label} · {option.ref}</option>)}</select></label>
              <label>Thinking<select value={detail.document.model.thinkingEffort} onChange={(event) => updateAndSave((document) => ({ ...document, model: { ...document.model, thinkingEffort: event.target.value as ThinkingEffort } }))}>{(["default", "low", "medium", "high"] as const).map((effort) => <option key={effort}>{effort}</option>)}</select></label>
              {/* Ordered by strictness, and labelled with the same names the composer chip uses:
                a Hero's ceiling and a Run's mode are the same three values. */}
              <label>Permission ceiling<select value={detail.document.permissionCeiling} onChange={(event) => updateAndSave((document) => ({ ...document, permissionCeiling: event.target.value as HeroDocument["permissionCeiling"] }))}>{PERMISSION_CEILINGS.map((mode) => <option key={mode} value={mode}>{t(`permission.${mode}`)}</option>)}</select></label>
              <label className="wide">Skills<input value={detail.document.skills.map((skill) => skill.id).join(", ")} onChange={(event) => updateLocal((document) => ({ ...document, skills: event.target.value.split(",").map((id) => id.trim()).filter(Boolean).map((id) => ({ id, mode: "available" as const })) }))} onBlur={() => void save(detail.document)} placeholder="report-writing, code-review" /></label>
            </div></section>
            <section><h3>Execution context</h3><div className="global-hero-grid">
              <label>Default mode<select aria-label="Default context mode" value={detail.document.context.defaultMode} onChange={(event) => updateAndSave((document) => ({ ...document, context: { ...document.context, defaultMode: event.target.value as HeroContextMode } }))}>{detail.document.context.allowedModes.map((mode) => <option key={mode} value={mode}>{ROLE_CONTEXT_LABELS[mode]}</option>)}</select></label>
              <div className="global-hero-modes wide">
                <span>Allowed modes</span>
                <div role="group" aria-label="Allowed context modes">
                  {ROLE_CONTEXT_MODES.map((mode) => {
                    const checked = detail.document.context.allowedModes.includes(mode);
                    return <label key={mode} className="global-hero-mode"><input type="checkbox" checked={checked} disabled={checked && detail.document.context.allowedModes.length === 1} onChange={(event) => toggleContextMode(mode, event.target.checked)} />{ROLE_CONTEXT_LABELS[mode]}</label>;
                  })}
                </div>
                <p className="global-hero-hint">Task only is required for Heroes that run as delegated children. Reply enables addressing a specific message; Room shares the session lineage; Fresh starts from the addressed request.</p>
              </div>
            </div></section>
            <section><h3>Hero prompt</h3><label className="global-hero-prompt"><textarea aria-label="Hero prompt" value={detail.document.prompt} onChange={(event) => updateLocal((document) => ({ ...document, prompt: event.target.value }))} onBlur={() => void save(detail.document)} /></label></section>
          </div>
          {diskDetail && <div className="hero-conflict-dialog" role="dialog" aria-modal="true" aria-label="Hero file diff">
            <div><header><strong>Local edits</strong><button type="button" onClick={() => setDiskDetail(null)}>Close</button></header><pre>{JSON.stringify(detail.document, null, 2)}</pre></div>
            <div><header><strong>File on disk</strong></header><pre>{JSON.stringify(diskDetail.document, null, 2)}</pre></div>
          </div>}
        </>}
      </section>
    </div>
  );
}
