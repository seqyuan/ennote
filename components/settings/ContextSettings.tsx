"use client";

import type { FormEvent } from "react";
import { useEffect, useState, useCallback } from "react";
import { X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import type { ModelProfile, PolicyProfile, Session, StandingApproval } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";
import { useSettingsStatus } from "@/components/settings/SettingsStatus";
import { SettingsList, SettingsRow, SettingsSection } from "@/components/settings/SettingsPrimitives";

interface ModelOverride {
  modelProfileId: string;
  triggerRatio?: number;
  tailMaxTokens?: number;
  summaryMaxOutputTokens?: number;
}

export function ContextSettings({ policies, models, session, refresh, setError, onSessionUpdated }: {
  policies: PolicyProfile[];
  models: ModelProfile[];
  session?: Session;
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
  onSessionUpdated: (session: Session) => void;
}) {
  const { markSaving, markSaved } = useSettingsStatus();
  async function createCompactionPolicy(name: string, config: Record<string, unknown>): Promise<boolean> {
    markSaving();
    try {
      await apiFetch("/v1/policy-profiles", { method: "POST", body: JSON.stringify({ name, kind: "compaction", config }) });
      setError(null);
      await refresh();
      markSaved();
      return true;
    } catch (reason) {
      setError((reason as Error).message);
      return false;
    }
  }

  async function setDefault(policyId: string) {
    markSaving();
    try {
      await apiFetch(`/v1/policy-profiles/${encodeURIComponent(policyId)}/default`, { method: "PUT" });
      setError(null);
      await refresh();
      markSaved();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  async function patchSession(body: Record<string, unknown>) {
    if (!session) return;
    markSaving();
    try {
      const updated = await apiFetch<Session>(`/v1/sessions/${encodeURIComponent(session.id)}`, {
        method: "PATCH", body: JSON.stringify(body),
      });
      onSessionUpdated(updated);
      setError(null);
      markSaved();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  const t = useT();

  return <section className="settings-tab-section" aria-labelledby="settings-context-heading">
    <header><h2 id="settings-context-heading">{t("settings.context.title")}</h2>
      <p>{t("settings.context.desc")}</p></header>
    <CompactionPolicyEditor policies={policies} models={models} onCreate={createCompactionPolicy} onDefault={setDefault} />
    <SettingsSection title={t("settings.context.currentSession")} description={session ? session.title : t("settings.context.selectSession")} className="current-session-settings">
      <div className="session-default-grid">
        <label className="session-model-select">{t("settings.context.defaultModel")}<select disabled={!session}
          value={session?.defaultModelProfileId ?? ""}
          onChange={event => void patchSession({ defaultModelProfileId: event.target.value || null })}>
          <option value="">{t("settings.context.useGlobalDefault")}</option>{models.map(model =>
            <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}
        </select></label>
        <label className="session-model-select">{t("settings.context.compaction")}<select disabled={!session}
          value={session?.compactionPolicyProfileId ?? ""}
          onChange={event => void patchSession({ compactionPolicyProfileId: event.target.value || null })}>
          <option value="">{t("settings.context.useGlobalDefault")}</option>{policies.filter(policy => policy.kind === "compaction" && policy.status === "active").map(policy =>
            <option key={policy.id} value={policy.id}>{policy.name} v{policy.version}</option>)}
        </select></label>
      </div>
    </SettingsSection>
    <StandingApprovalsSection sessionId={session?.id ?? null} />
  </section>;
}

function CompactionPolicyEditor({ policies, models, onCreate, onDefault }: {
  policies: PolicyProfile[];
  models: ModelProfile[];
  onCreate: (name: string, config: Record<string, unknown>) => Promise<boolean>;
  onDefault: (policyId: string) => Promise<void>;
}) {
  const t = useT();
  const [modelPolicies, setModelPolicies] = useState<ModelOverride[]>([]);
  const addOverride = () => setModelPolicies((current) => [...current, { modelProfileId: "" }]);
  const removeOverride = (index: number) => setModelPolicies((current) => current.filter((_, i) => i !== index));
  const updateOverride = (index: number, patch: Partial<ModelOverride>) =>
    setModelPolicies((current) => current.map((row, i) => (i === index ? { ...row, ...patch } : row)));

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const mode = String(data.get("mode"));
    const overflow = mode === "manual_and_auto" && data.get("allowOverflowRecovery") === "on";
    const modelPolicyPayload = modelPolicies
      .filter((row) => row.modelProfileId)
      .map((row) => ({
        modelProfileId: row.modelProfileId,
        ...(row.triggerRatio !== undefined ? { triggerRatio: row.triggerRatio } : {}),
        ...(row.tailMaxTokens !== undefined ? { tailMaxTokens: row.tailMaxTokens } : {}),
        ...(row.summaryMaxOutputTokens !== undefined ? { summaryMaxOutputTokens: row.summaryMaxOutputTokens } : {}),
      }));
    // A refused create keeps every field: the name, the ratios and the per-model
    // overrides are cleared only once the Worker accepted the version.
    const created = await onCreate(String(data.get("name")), {
      mode,
      triggerRatio: Number(data.get("triggerRatio")),
      keepRecentTurns: Number(data.get("keepRecentTurns")),
      tailTokenRatio: Number(data.get("tailTokenRatio")),
      tailMinTokens: Number(data.get("tailMinTokens")),
      tailMaxTokens: Number(data.get("tailMaxTokens")),
      summaryInputRatio: Number(data.get("summaryInputRatio")),
      compactionModelProfileId: data.get("compactionModelProfileId") || null,
      summaryMaxOutputTokens: Number(data.get("summaryMaxOutputTokens")),
      ...(modelPolicyPayload.length > 0 ? { modelPolicies: modelPolicyPayload } : {}),
      includeReasoning: data.get("includeReasoning") === "on",
      allowHistoryLookup: data.get("allowHistoryLookup") === "on",
      allowOverflowRecovery: overflow,
      maxOverflowRecoveries: overflow ? 1 : 0,
      ineffectiveReclaimRatio: Number(data.get("ineffectiveReclaimRatio")),
      ineffectiveLimit: Number(data.get("ineffectiveLimit")),
      failureCooldownSeconds: Number(data.get("failureCooldownSeconds")),
      promptVersion: "v1",
    });
    if (!created) return;
    form.reset();
    setModelPolicies([]);
  }

  return <SettingsSection title={t("settings.context.editorTitle")} description={t("settings.context.editorDescription")}>
    <form className="settings-form compaction-form" onSubmit={submit}>
      <label>{t("settings.context.name")}<input name="name" required /></label>
      <label>{t("settings.context.mode")}<select name="mode" defaultValue="manual_only"><option value="disabled">{t("settings.context.mode.disabled")}</option>
        <option value="manual_only">{t("settings.context.mode.manualOnly")}</option><option value="manual_and_auto">{t("settings.context.mode.manualAndAuto")}</option></select></label>
      <label>{t("settings.context.summaryModel")}<select name="compactionModelProfileId" defaultValue=""><option value="">{t("settings.context.useRunModel")}</option>
        {models.map(model => <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}</select></label>
      <label>{t("settings.context.triggerRatio")}<input name="triggerRatio" type="number" min="0.01" max="0.99" step="0.01" defaultValue="0.75" required /></label>
      <label>{t("settings.context.recentTurns")}<input name="keepRecentTurns" type="number" min="1" step="1" defaultValue="2" required /></label>
      <label>{t("settings.context.tailRatio")}<input name="tailTokenRatio" type="number" min="0.01" max="0.99" step="0.01" defaultValue="0.2" required /></label>
      <label>{t("settings.context.tailMinimum")}<input name="tailMinTokens" type="number" min="1" step="1" defaultValue="8000" required /></label>
      <label>{t("settings.context.tailMaximum")}<input name="tailMaxTokens" type="number" min="1" step="1" defaultValue="32000" required /></label>
      <label>{t("settings.context.summaryInputRatio")}<input name="summaryInputRatio" type="number" min="0.01" max="0.99" step="0.01" defaultValue="0.7" required /></label>
      <label>{t("settings.context.summaryOutput")}<input name="summaryMaxOutputTokens" type="number" min="1" step="1" defaultValue="4096" required /></label>
      <label>{t("settings.context.cooldownSeconds")}<input name="failureCooldownSeconds" type="number" min="0" step="1" defaultValue="600" required /></label>
      <label>{t("settings.context.ineffectiveRatio")}<input name="ineffectiveReclaimRatio" type="number" min="0" max="0.99" step="0.01" defaultValue="0.1" required /></label>
      <label>{t("settings.context.ineffectiveLimit")}<input name="ineffectiveLimit" type="number" min="1" step="1" defaultValue="3" required /></label>
      <div className="settings-toggles compaction-toggles">
        <label><input name="allowHistoryLookup" type="checkbox" defaultChecked /> {t("settings.context.allowHistoryLookup")}</label>
        <label><input name="allowOverflowRecovery" type="checkbox" defaultChecked /> {t("settings.context.allowOverflowRecovery")}</label>
        <label><input name="includeReasoning" type="checkbox" /> {t("settings.context.includeReasoning")}</label>
      </div>
      <div className="settings-subsection compaction-model-policies">
        <header><h4>{t("settings.context.overridesTitle")}</h4><p>{t("settings.context.overridesDescription")}</p></header>
        {modelPolicies.map((row, index) => (
          <div key={index} className="model-policy-row">
            <select value={row.modelProfileId} onChange={(event) => updateOverride(index, { modelProfileId: event.target.value })} aria-label={t("settings.context.overrideModelAria", { index: String(index + 1) })}>
              <option value="">{t("settings.context.selectModel")}</option>
              {models.map((model) => <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}
            </select>
            <input type="number" step="0.01" min="0.01" max="0.99" placeholder={t("settings.context.triggerRatio")} value={row.triggerRatio ?? ""}
              onChange={(event) => updateOverride(index, { triggerRatio: event.target.value === "" ? undefined : Number(event.target.value) })} />
            <input type="number" step="1" min="1" placeholder={t("settings.context.overrideTailMax")} value={row.tailMaxTokens ?? ""}
              onChange={(event) => updateOverride(index, { tailMaxTokens: event.target.value === "" ? undefined : Number(event.target.value) })} />
            <input type="number" step="1" min="1" placeholder={t("settings.context.overrideSummaryOutput")} value={row.summaryMaxOutputTokens ?? ""}
              onChange={(event) => updateOverride(index, { summaryMaxOutputTokens: event.target.value === "" ? undefined : Number(event.target.value) })} />
            <button type="button" className="secondary-btn" onClick={() => removeOverride(index)} aria-label={t("settings.context.removeOverride", { index: String(index + 1) })}><X size={12} aria-hidden="true" /></button>
          </div>
        ))}
        <button type="button" className="secondary-btn" onClick={addOverride}>{t("settings.context.addOverride")}</button>
      </div>
      <button type="submit">{t("settings.context.createVersion")}</button>
    </form>
    <div className="settings-list">{policies.filter(policy => policy.kind === "compaction").map(policy => <div className="settings-row" key={policy.id}>
      <div><strong>{policy.name}</strong><span>v{policy.version} · {String(policy.config.mode ?? "configured")}</span></div>
      <button className="secondary-btn" disabled={policy.status !== "active"} onClick={() => onDefault(policy.id)}>{t("settings.context.useAsDefault")}</button>
    </div>)}</div>
  </SettingsSection>;
}

function StandingApprovalsSection({ sessionId }: { sessionId: string | null }) {
  const t = useT();
  const { markSaving, markSaved } = useSettingsStatus();
  const [rules, setRules] = useState<StandingApproval[]>([]);
  const [loadedSessionID, setLoadedSessionID] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  useEffect(() => {
    if (!sessionId) return;
    let cancelled = false;
    void apiFetch<{ items: StandingApproval[] }>(`/v1/sessions/${encodeURIComponent(sessionId)}/standing-approvals`)
      .then((data) => {
        if (cancelled) return;
        setRules(data?.items ?? []);
        setLoadError(null);
        setLoadedSessionID(sessionId);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setRules([]);
        setLoadError((err as Error).message);
        setLoadedSessionID(sessionId);
      });
    return () => { cancelled = true; };
  }, [sessionId]);

  const loaded = loadedSessionID === sessionId;
  const visibleRules = loaded ? rules : [];
  const loading = !loaded;
  const error = loaded ? loadError : null;

  const revoke = useCallback(async (ruleId: string) => {
    if (!sessionId) return;
    markSaving();
    try {
      await apiFetch(`/v1/sessions/${encodeURIComponent(sessionId)}/standing-approvals/${encodeURIComponent(ruleId)}/revoke`, { method: "POST" });
      setRules(prev => prev.filter(r => r.id !== ruleId));
      setLoadError(null);
      markSaved();
    } catch (err) {
      setLoadError((err as Error).message);
    }
  }, [sessionId, markSaving, markSaved]);

  if (!sessionId) return null;

  return <SettingsSection title={t("settings.context.approvalsTitle")} description={t("settings.context.approvalsDescription")} className="standing-approvals-section">
    {loading && <div className="settings-empty">{t("settings.common.loading")}</div>}
    {!loading && error && <div className="settings-error">{error}</div>}
    {!loading && !error && visibleRules.length === 0 &&
      <div className="settings-empty">{t("settings.context.approvalsEmpty")}</div>}
    {!loading && visibleRules.length > 0 && <SettingsList>
      {visibleRules.map(rule => <SettingsRow
        key={rule.id}
        title={rule.toolName}
        meta={rule.scopeDisplay}
        actions={<button className="secondary-btn" onClick={() => revoke(rule.id)}>{t("settings.context.revoke")}</button>}
      />)}
    </SettingsList>}
  </SettingsSection>;
}
