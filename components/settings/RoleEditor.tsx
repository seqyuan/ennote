"use client";

import { Archive, CheckCircle2, History, Save, Upload } from "lucide-react";
import { useMemo, useState } from "react";
import { ModelRuntimeControl } from "@/components/ModelRuntimeControl";
import { SkillPicker, type RoleSkillBinding } from "@/components/settings/SkillPicker";
import type { ModelProfile, RoleIdentity, RoleValidationResult, RoleVersion } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type RoleDefinition = components["schemas"]["RoleDefinition"];
type RoleContextMode = RoleDefinition["contextPolicy"]["defaultMode"];
type PermissionCeiling = RoleDefinition["permissionCeiling"];
type Authority = RoleDefinition["authority"];
type Admission = RoleDefinition["delegationPolicy"]["admission"];

const roleTools = ["read", "ls", "grep", "find", "bash", "write", "edit", "web_fetch", "publish_artifact"];
const roleColors = ["#2563eb", "#0f766e", "#b45309", "#b91c1c", "#7c3aed", "#475569"];

export function RoleEditor({ role, models, onSaved, onArchived, setError }: {
  role: RoleIdentity;
  models: ModelProfile[];
  onSaved: (role: RoleIdentity) => void;
  onArchived: (role: RoleIdentity) => void;
  setError: (message: string | null) => void;
}) {
  const [name, setName] = useState(role.name);
  const [handle, setHandle] = useState(role.handle);
  const [description, setDescription] = useState(role.description);
  const [positioning, setPositioning] = useState(role.positioning);
  const [icon, setIcon] = useState(role.icon);
  const [color, setColor] = useState(role.color.startsWith("#") ? role.color : roleColors[0]);
  const [definition, setDefinition] = useState<RoleDefinition>(role.draft);
  const [validation, setValidation] = useState<RoleValidationResult | null>(null);
  const [versions, setVersions] = useState<RoleVersion[] | null>(null);
  const [busy, setBusy] = useState<"save" | "validate" | "publish" | "archive" | null>(null);

  const dirty = useMemo(() => name !== role.name || handle !== role.handle || description !== role.description
    || positioning !== role.positioning || icon !== role.icon || color !== role.color
    || JSON.stringify(definition) !== JSON.stringify(role.draft),
  [color, definition, description, handle, icon, name, positioning, role]);

  function updateDefinition(change: (current: RoleDefinition) => RoleDefinition) {
    setDefinition((current) => change(current));
    setValidation(null);
  }

  async function saveDraft() {
    setBusy("save");
    try {
      const updated = await apiFetch<RoleIdentity>(`/v1/roles/${encodeURIComponent(role.id)}/draft`, {
        method: "PATCH",
        body: JSON.stringify({ expectedRevision: role.draftRevision, name, handle, description, positioning,
          icon, color, definition }),
      });
      setError(null);
      onSaved(updated);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  }

  async function validateDraft() {
    setBusy("validate");
    try {
      const result = await apiFetch<RoleValidationResult>(`/v1/roles/${encodeURIComponent(role.id)}/validate`, {
        method: "POST", body: "{}",
      });
      setValidation(result);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  }

  async function publish() {
    setBusy("publish");
    try {
      await apiFetch<RoleVersion>(`/v1/roles/${encodeURIComponent(role.id)}/publish`, {
        method: "POST", body: JSON.stringify({ expectedRevision: role.draftRevision }),
      });
      const updated = await apiFetch<RoleIdentity>(`/v1/roles/${encodeURIComponent(role.id)}`);
      setValidation(null);
      setVersions(null);
      setError(null);
      onSaved(updated);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  }

  async function archive() {
    setBusy("archive");
    try {
      const updated = await apiFetch<RoleIdentity>(`/v1/roles/${encodeURIComponent(role.id)}/archive`, {
        method: "POST", body: "{}",
      });
      setError(null);
      onArchived(updated);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  }

  async function loadVersions() {
    try {
      setVersions(await apiFetch<RoleVersion[]>(`/v1/roles/${encodeURIComponent(role.id)}/versions`));
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  const isPublishedDraft = role.currentVersionId && !dirty;

  return <div className="role-editor">
    <div className="role-editor-commandbar">
      <div className="role-editor-version">
        <span className="role-color-swatch" style={{ background: color }} aria-hidden="true" />
        <strong>{role.currentVersion ? `Published v${role.currentVersion}` : "Unpublished draft"}</strong>
        {dirty && <span>Unsaved</span>}
      </div>
      <div className="role-editor-actions">
        <button type="button" className="settings-btn" disabled={!dirty || Boolean(busy)} onClick={saveDraft}>
          <Save size={14} /> Save
        </button>
        <button type="button" className="settings-btn" disabled={dirty || Boolean(busy)} onClick={validateDraft}>
          <CheckCircle2 size={14} /> Validate
        </button>
        <button type="button" className="settings-btn primary" disabled={dirty || Boolean(busy)} onClick={publish}>
          <Upload size={14} /> Publish
        </button>
        <button type="button" className="settings-btn" onClick={loadVersions} title="Version history">
          <History size={14} aria-hidden="true" />
        </button>
        <button type="button" className="settings-btn danger" disabled={Boolean(busy)} onClick={archive} title="Archive Role">
          <Archive size={14} aria-hidden="true" />
        </button>
      </div>
    </div>

    {validation && <div className={validation.valid ? "role-validation valid" : "role-validation invalid"}>
      <strong>{validation.valid ? "Ready to publish" : "Validation failed"}</strong>
      {validation.diagnostics.map((diagnostic) => <span key={`${diagnostic.code}-${diagnostic.field}`}>
        {diagnostic.field ? `${diagnostic.field}: ` : ""}{diagnostic.message}
      </span>)}
    </div>}

    {versions && <div className="role-version-strip">
      {versions.length === 0 && <span>No published versions</span>}
      {versions.map((version) => <span key={version.id}>v{version.version} <code>{version.configDigest.slice(7, 15)}</code></span>)}
    </div>}

    <div className="role-editor-sections">
      <section>
        <header><h3>Identity</h3></header>
        <div className="role-field-grid identity-grid">
          <label>Name<input value={name} onChange={(event) => setName(event.target.value)} /></label>
          <label>Handle<input value={handle} pattern="[a-z][a-z0-9_-]{1,31}" onChange={(event) => setHandle(event.target.value)} /></label>
          <label>Icon<input value={icon} onChange={(event) => setIcon(event.target.value)} /></label>
          <label>Color<div className="role-color-field">
            <input type="color" value={color} onChange={(event) => setColor(event.target.value)} />
            <span>{color}</span>
          </div></label>
          <label className="wide">Description<input value={description} onChange={(event) => setDescription(event.target.value)} /></label>
          <label className="wide">Positioning<input value={positioning} onChange={(event) => setPositioning(event.target.value)} /></label>
        </div>
      </section>

      <section>
        <header><h3>Runtime</h3></header>
        <ModelRuntimeControl models={models}
          modelProfileId={definition.modelBinding.modelProfileId ?? ""}
          thinkingEffort={definition.modelBinding.thinkingEffort}
          onModelChange={(modelProfileId) => updateDefinition((current) => ({ ...current,
            modelBinding: { ...current.modelBinding, mode: "fixed", modelProfileId } }))}
          onEffortChange={(thinkingEffort) => updateDefinition((current) => ({ ...current,
            modelBinding: { ...current.modelBinding, thinkingEffort } }))} />
        <label className="role-number-field">Loop limit<input type="number" min={1} max={64}
          value={definition.maxLoopIterations}
          onChange={(event) => updateDefinition((current) => ({ ...current, maxLoopIterations: Number(event.target.value) }))} /></label>
      </section>

      <section>
        <header><h3>Instructions</h3></header>
        <label className="role-prompt-field">Role prompt
          <textarea value={definition.rolePrompt}
            onChange={(event) => updateDefinition((current) => ({ ...current, rolePrompt: event.target.value }))} />
        </label>
      </section>

      <section>
        <header><h3>Authority</h3></header>
        <div className="role-segmented-row">
          <label>Authority<select value={definition.authority}
            onChange={(event) => updateDefinition((current) => ({ ...current, authority: event.target.value as Authority }))}>
            <option value="read_only">Read only</option><option value="mutation">Mutation</option>
          </select></label>
          <label>Permission ceiling<select value={definition.permissionCeiling}
            onChange={(event) => updateDefinition((current) => ({ ...current, permissionCeiling: event.target.value as PermissionCeiling }))}>
            <option value="discuss">Discuss</option><option value="ask">Ask</option><option value="auto">Auto</option>
          </select></label>
        </div>
        <div className="role-tool-grid">
          {roleTools.map((tool) => <label key={tool}><input type="checkbox" checked={definition.allowedTools.includes(tool)}
            onChange={(event) => updateDefinition((current) => ({ ...current,
              allowedTools: event.target.checked
                ? [...current.allowedTools, tool]
                : current.allowedTools.filter((candidate) => candidate !== tool),
            }))} /> {tool}</label>)}
        </div>
      </section>

      <section>
        <header><h3>Context & output</h3></header>
        <div className="role-segmented-row">
          <label>Default context<select value={definition.contextPolicy.defaultMode}
            onChange={(event) => {
              const mode = event.target.value as RoleContextMode;
              updateDefinition((current) => ({ ...current, contextPolicy: { ...current.contextPolicy,
                defaultMode: mode, allowedModes: Array.from(new Set([...current.contextPolicy.allowedModes, mode])) } }));
            }}>
            <option value="room">Room</option><option value="reply">Reply</option><option value="fresh">Fresh</option>
          </select></label>
          <label>Output contract<select value={definition.outputContract}
            onChange={(event) => updateDefinition((current) => ({ ...current, outputContract: event.target.value }))}>
            <option value="text-v1">Text</option><option value="structured-v1">Structured</option>
          </select></label>
        </div>
        <div className="role-tool-grid">
          {(["room", "reply", "fresh"] as RoleContextMode[]).map((mode) => <label key={mode}>
            <input type="checkbox" checked={definition.contextPolicy.allowedModes.includes(mode)}
              disabled={definition.contextPolicy.defaultMode === mode}
              onChange={(event) => updateDefinition((current) => ({ ...current, contextPolicy: { ...current.contextPolicy,
                allowedModes: event.target.checked
                  ? [...current.contextPolicy.allowedModes, mode]
                  : current.contextPolicy.allowedModes.filter((candidate) => candidate !== mode),
              } }))} /> {mode}
          </label>)}
        </div>
        <SkillPicker
          entries={(definition.skills?.entries ?? []).map((entry) => ({
            skillId: entry.skillId,
            mode: entry.mode === "preload" ? "preload" : "available",
          }))}
          onEntries={(entries: RoleSkillBinding[]) => updateDefinition((current) => ({ ...current,
            skills: { entries: entries.map((entry) => ({ skillId: entry.skillId, mode: entry.mode })) } }))}
        />
      </section>

      <section>
        <header><h3>Delegation policy</h3></header>
        <div className="role-segmented-row">
          <label>Admission<select value={definition.delegationPolicy.admission}
            onChange={(event) => updateDefinition((current) => ({ ...current, delegationPolicy: { ...current.delegationPolicy,
              admission: event.target.value as Admission } }))}>
            <option value="denied">Denied</option><option value="approval_required">Approval required</option>
            <option value="auto_within_budget">Auto within budget</option>
          </select></label>
          <label>Concurrent instances<input type="number" min={1} max={8}
            value={definition.delegationPolicy.maxConcurrentInstances}
            onChange={(event) => updateDefinition((current) => ({ ...current, delegationPolicy: { ...current.delegationPolicy,
              maxConcurrentInstances: Number(event.target.value) } }))} /></label>
          <label>Token ceiling<input type="number" min={0}
            value={definition.delegationPolicy.budgetCeiling.maxTotalTokens}
            onChange={(event) => updateDefinition((current) => ({ ...current, delegationPolicy: { ...current.delegationPolicy,
              budgetCeiling: { ...current.delegationPolicy.budgetCeiling, maxTotalTokens: Number(event.target.value) } } }))} /></label>
        </div>
      </section>
    </div>
    {isPublishedDraft && <span className="role-editor-saved-state">Published configuration is immutable</span>}
  </div>;
}
