"use client";

import { ArrowLeft, Bot, Check, FileText, Loader2, Plus, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { modelOptions } from "@/components/graphs/types";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

type ThinkingEffort = "default" | "low" | "medium" | "high";

interface RoleDocument {
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
  context: { defaultMode: "room" | "fresh"; allowedModes: Array<"room" | "fresh">; ownExecutionContinuity: "none" };
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

interface RoleSummary { id: string; name: string; path: string; digest?: string; error?: string }
interface RoleDetail { id: string; name: string; path: string; digest: string; document: RoleDocument }

function defaultRole(handle: string, name: string, model: string): RoleDocument {
  return {
    schemaVersion: 1, handle, name, description: "", positioning: "", icon: "bot", color: "neutral",
    model: { ref: model, thinkingEffort: "default", fallbacks: [] }, skills: [],
    authority: "read_only", permissionCeiling: "discuss", allowedTools: ["read", "ls", "grep", "find"],
    context: { defaultMode: "room", allowedModes: ["room", "fresh"], ownExecutionContinuity: "none" },
    delegation: {
      admission: "auto_within_budget", allowedCallerKinds: ["host"], allowedStrategies: ["single", "parallel"],
      maxInvocationsPerParentRun: 16, maxConcurrentInstances: 16,
      budgetCeiling: { maxModelCalls: 4, maxToolCalls: 8, maxTotalTokens: 20000, maxOutputTokens: 4000, maxCostUsdMicros: 100000, maxWallTimeMs: 120000 },
    },
    outputContract: "text-v1", maxLoopIterations: 8,
    prompt: "Perform the requested work independently. Distinguish evidence from assumptions and report a concise result.",
  };
}

export function RolesSettings({ models, providers, setError }: {
  models: ModelProfile[];
  providers: ProviderProfile[];
  setError: (value: string | null) => void;
}) {
  const options = useMemo(() => modelOptions(models, providers), [models, providers]);
  const [roles, setRoles] = useState<RoleSummary[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<RoleDetail | null>(null);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<"loading" | "saving" | "creating" | "publishing" | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [newRole, setNewRole] = useState({ id: "", name: "" });
  const [conflict, setConflict] = useState(false);
  const [diskDetail, setDiskDetail] = useState<RoleDetail | null>(null);

  const refresh = useCallback(async () => {
    const next = await apiFetch<RoleSummary[]>("/v1/global-roles");
    setRoles(next);
    return next;
  }, []);

  const openRole = useCallback(async (id: string) => {
    setBusy("loading");
    setSelected(id);
    try {
      setDetail(await apiFetch<RoleDetail>(`/v1/global-roles/${encodeURIComponent(id)}`));
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
        const first = items.find((role) => !role.error);
        if (!cancelled && first) void openRole(first.id);
      }).catch((reason: unknown) => { if (!cancelled) setError((reason as Error).message); });
    }, 0);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [openRole, refresh, setError]);

  const createRole = async () => {
    if (!options[0]) { setError("Create an active model before creating a Role."); return; }
    const id = newRole.id.trim();
    const name = newRole.name.trim();
    if (!id || !name) return;
    setBusy("creating");
    try {
      const created = await apiFetch<RoleDetail>("/v1/global-roles", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ document: defaultRole(id, name, options[0].ref) }),
      });
      setCreateOpen(false);
      setNewRole({ id: "", name: "" });
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

  const save = async (document: RoleDocument) => {
    if (!detail || busy) return;
    setBusy("saving");
    try {
      const updated = await apiFetch<RoleDetail>(`/v1/global-roles/${encodeURIComponent(detail.id)}`, {
        method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expectedDigest: detail.digest, document }),
      });
      setDetail(updated);
      setRoles((current) => current.map((role) => role.id === updated.id ? { ...role, name: updated.name, digest: updated.digest, error: undefined } : role));
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
      setDiskDetail(await apiFetch<RoleDetail>(`/v1/global-roles/${encodeURIComponent(detail.id)}`));
    } catch (reason) {
      setError((reason as Error).message);
    }
  };

  const publish = async () => {
    if (!detail || busy) return;
    setBusy("publishing");
    try {
      await apiFetch(`/v1/global-roles/${encodeURIComponent(detail.id)}/publish`, { method: "POST" });
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const updateLocal = (update: (document: RoleDocument) => RoleDocument) => {
    if (!detail) return;
    setDetail({ ...detail, document: update(detail.document) });
  };
  const updateAndSave = (update: (document: RoleDocument) => RoleDocument) => {
    if (!detail) return;
    const document = update(detail.document);
    setDetail({ ...detail, document });
    void save(document);
  };

  const visible = roles.filter((role) => `${role.name} ${role.id}`.toLowerCase().includes(query.trim().toLowerCase()));

  return (
    <div className={`global-roles ${selected ? "has-selection" : ""}`}>
      <aside className="global-roles-list">
        <header><div><strong>Role catalog</strong><span>{roles.length} global Roles</span></div><button type="button" onClick={() => setCreateOpen((value) => !value)} aria-label="Add Role"><Plus size={18} /></button></header>
        {createOpen && <div className="global-role-create"><input value={newRole.id} onChange={(event) => setNewRole((value) => ({ ...value, id: event.target.value }))} placeholder="role-id" /><input value={newRole.name} onChange={(event) => setNewRole((value) => ({ ...value, name: event.target.value }))} placeholder="Role name" /><button type="button" onClick={() => void createRole()} disabled={busy === "creating"}>{busy === "creating" ? "Creating…" : "Create"}</button></div>}
        <label className="global-role-search"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search Roles" /></label>
        <div role="listbox" aria-label="Global Roles">
          {visible.map((role) => <button key={role.id} type="button" role="option" aria-selected={selected === role.id} onClick={() => void openRole(role.id)}><Bot size={17} /><span><strong>{role.name}</strong><code>{role.id}</code>{role.error && <small>{role.error}</small>}</span><FileText size={14} /></button>)}
          {visible.length === 0 && <div className="roles-empty">No global Roles</div>}
        </div>
      </aside>

      <section className="global-role-detail">
        {!detail && busy === "loading" && <div className="roles-detail-empty"><Loader2 className="spin" size={18} /> Loading Role…</div>}
        {!detail && busy !== "loading" && <div className="roles-detail-empty"><Bot size={26} /> Select or create a Role</div>}
        {detail && <>
          <header className="global-role-commandbar">
            <button type="button" className="global-role-back" onClick={() => { setSelected(null); setDetail(null); }}><ArrowLeft size={17} /> Roles</button>
            <div><strong>{detail.document.name}</strong><code>{detail.path}</code></div>
            <div className="global-role-command-actions">
              {conflict ? <>
                <span className="role-conflict-state">File changed on disk</span>
                <button type="button" onClick={() => void showDiff()}>Diff</button>
                <button type="button" onClick={() => void openRole(detail.id)}>Reload</button>
              </> : <span className={busy === "saving" ? "is-saving" : ""}>{busy === "saving" ? <><Loader2 className="spin" size={14} /> Saving</> : <><Check size={14} /> Saved</>}</span>}
              <button type="button" onClick={() => void publish()} disabled={Boolean(busy) || conflict}>{busy === "publishing" ? "Publishing…" : "Publish"}</button>
            </div>
          </header>
          <div className="global-role-form">
            <section><h3>Identity</h3><div className="global-role-grid">
              <label>Name<input value={detail.document.name} onChange={(event) => updateLocal((document) => ({ ...document, name: event.target.value }))} onBlur={() => void save(detail.document)} /></label>
              <label>Handle<input value={detail.document.handle} disabled /></label>
              <label className="wide">Description<input value={detail.document.description} onChange={(event) => updateLocal((document) => ({ ...document, description: event.target.value }))} onBlur={() => void save(detail.document)} /></label>
            </div></section>
            <section><h3>Runtime</h3><div className="global-role-grid">
              <label className="wide">Model<select value={detail.document.model.ref} onChange={(event) => updateAndSave((document) => ({ ...document, model: { ...document.model, ref: event.target.value } }))}>{options.map((option) => <option key={option.ref} value={option.ref}>{option.label} · {option.ref}</option>)}</select></label>
              <label>Thinking<select value={detail.document.model.thinkingEffort} onChange={(event) => updateAndSave((document) => ({ ...document, model: { ...document.model, thinkingEffort: event.target.value as ThinkingEffort } }))}>{(["default", "low", "medium", "high"] as const).map((effort) => <option key={effort}>{effort}</option>)}</select></label>
              <label>Permission ceiling<select value={detail.document.permissionCeiling} onChange={(event) => updateAndSave((document) => ({ ...document, permissionCeiling: event.target.value as RoleDocument["permissionCeiling"] }))}><option value="discuss">Discuss</option><option value="auto">Auto</option><option value="ask">Ask</option></select></label>
              <label className="wide">Skills<input value={detail.document.skills.map((skill) => skill.id).join(", ")} onChange={(event) => updateLocal((document) => ({ ...document, skills: event.target.value.split(",").map((id) => id.trim()).filter(Boolean).map((id) => ({ id, mode: "available" as const })) }))} onBlur={() => void save(detail.document)} placeholder="report-writing, code-review" /></label>
            </div></section>
            <section><h3>Role prompt</h3><label className="global-role-prompt"><textarea aria-label="Role prompt" value={detail.document.prompt} onChange={(event) => updateLocal((document) => ({ ...document, prompt: event.target.value }))} onBlur={() => void save(detail.document)} /></label></section>
          </div>
          {diskDetail && <div className="role-conflict-dialog" role="dialog" aria-modal="true" aria-label="Role file diff">
            <div><header><strong>Local edits</strong><button type="button" onClick={() => setDiskDetail(null)}>Close</button></header><pre>{JSON.stringify(detail.document, null, 2)}</pre></div>
            <div><header><strong>File on disk</strong></header><pre>{JSON.stringify(diskDetail.document, null, 2)}</pre></div>
          </div>}
        </>}
      </section>
    </div>
  );
}
