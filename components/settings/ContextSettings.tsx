"use client";

import type { FormEvent } from "react";
import { useEffect, useState, useCallback } from "react";
import type { ModelProfile, PolicyProfile, Session, StandingApproval } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

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
  async function createCompactionPolicy(name: string, config: Record<string, unknown>) {
    try {
      await apiFetch("/v1/policy-profiles", { method: "POST", body: JSON.stringify({ name, kind: "compaction", config }) });
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  async function setDefault(policyId: string) {
    try {
      await apiFetch(`/v1/policy-profiles/${encodeURIComponent(policyId)}/default`, { method: "PUT" });
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  async function patchSession(body: Record<string, unknown>) {
    if (!session) return;
    try {
      const updated = await apiFetch<Session>(`/v1/sessions/${encodeURIComponent(session.id)}`, {
        method: "PATCH", body: JSON.stringify(body),
      });
      onSessionUpdated(updated);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  return <section className="settings-tab-section" aria-labelledby="settings-context-heading">
    <header><h2 id="settings-context-heading">Context &amp; session</h2>
      <p>Compaction policy versions and defaults frozen into the next Session Run.</p></header>
    <CompactionPolicyEditor policies={policies} models={models} onCreate={createCompactionPolicy} onDefault={setDefault} />
    <section className="settings-subsection current-session-settings">
      <header><h3>Current session</h3><p>{session ? session.title : "Select a Session to configure its defaults."}</p></header>
      <div className="session-default-grid">
        <label className="session-model-select">Default model<select disabled={!session}
          value={session?.defaultModelProfileId ?? ""}
          onChange={event => void patchSession({ defaultModelProfileId: event.target.value || null })}>
          <option value="">Use global default</option>{models.map(model =>
            <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}
        </select></label>
        <label className="session-model-select">Context compaction<select disabled={!session}
          value={session?.compactionPolicyProfileId ?? ""}
          onChange={event => void patchSession({ compactionPolicyProfileId: event.target.value || null })}>
          <option value="">Use global default</option>{policies.filter(policy => policy.kind === "compaction" && policy.status === "active").map(policy =>
            <option key={policy.id} value={policy.id}>{policy.name} v{policy.version}</option>)}
        </select></label>
      </div>
    </section>
    <StandingApprovalsSection sessionId={session?.id ?? null} />
  </section>;
}

function CompactionPolicyEditor({ policies, models, onCreate, onDefault }: {
  policies: PolicyProfile[];
  models: ModelProfile[];
  onCreate: (name: string, config: Record<string, unknown>) => Promise<void>;
  onDefault: (policyId: string) => Promise<void>;
}) {
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
    await onCreate(String(data.get("name")), {
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
    form.reset();
    setModelPolicies([]);
  }

  return <section className="settings-subsection">
    <header><h3>Context compaction</h3><p>Manual and bounded automatic context checkpoint behavior.</p></header>
    <form className="settings-form compaction-form" onSubmit={submit}>
      <label>Name<input name="name" required /></label>
      <label>Mode<select name="mode" defaultValue="manual_only"><option value="disabled">Disabled</option>
        <option value="manual_only">Manual only</option><option value="manual_and_auto">Manual and automatic</option></select></label>
      <label>Summary model<select name="compactionModelProfileId" defaultValue=""><option value="">Use run model</option>
        {models.map(model => <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}</select></label>
      <label>Trigger ratio<input name="triggerRatio" type="number" min="0.01" max="0.99" step="0.01" defaultValue="0.75" required /></label>
      <label>Recent turns<input name="keepRecentTurns" type="number" min="1" step="1" defaultValue="2" required /></label>
      <label>Tail ratio<input name="tailTokenRatio" type="number" min="0.01" max="0.99" step="0.01" defaultValue="0.2" required /></label>
      <label>Tail minimum<input name="tailMinTokens" type="number" min="1" step="1" defaultValue="8000" required /></label>
      <label>Tail maximum<input name="tailMaxTokens" type="number" min="1" step="1" defaultValue="32000" required /></label>
      <label>Summary input ratio<input name="summaryInputRatio" type="number" min="0.01" max="0.99" step="0.01" defaultValue="0.7" required /></label>
      <label>Summary output<input name="summaryMaxOutputTokens" type="number" min="1" step="1" defaultValue="4096" required /></label>
      <label>Cooldown seconds<input name="failureCooldownSeconds" type="number" min="0" step="1" defaultValue="600" required /></label>
      <label>Ineffective ratio<input name="ineffectiveReclaimRatio" type="number" min="0" max="0.99" step="0.01" defaultValue="0.1" required /></label>
      <label>Ineffective limit<input name="ineffectiveLimit" type="number" min="1" step="1" defaultValue="3" required /></label>
      <div className="settings-toggles compaction-toggles">
        <label><input name="allowHistoryLookup" type="checkbox" defaultChecked /> History lookup</label>
        <label><input name="allowOverflowRecovery" type="checkbox" defaultChecked /> Overflow recovery</label>
        <label><input name="includeReasoning" type="checkbox" /> Include reasoning</label>
      </div>
      <div className="settings-subsection compaction-model-policies">
        <header><h4>Per-model overrides</h4><p>Optional budget overrides for a specific model.</p></header>
        {modelPolicies.map((row, index) => (
          <div key={index} className="model-policy-row">
            <select value={row.modelProfileId} onChange={(event) => updateOverride(index, { modelProfileId: event.target.value })} aria-label={`Override model ${index + 1}`}>
              <option value="">Select model…</option>
              {models.map((model) => <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}
            </select>
            <input type="number" step="0.01" min="0.01" max="0.99" placeholder="Trigger ratio" value={row.triggerRatio ?? ""}
              onChange={(event) => updateOverride(index, { triggerRatio: event.target.value === "" ? undefined : Number(event.target.value) })} />
            <input type="number" step="1" min="1" placeholder="Tail max tokens" value={row.tailMaxTokens ?? ""}
              onChange={(event) => updateOverride(index, { tailMaxTokens: event.target.value === "" ? undefined : Number(event.target.value) })} />
            <input type="number" step="1" min="1" placeholder="Summary output" value={row.summaryMaxOutputTokens ?? ""}
              onChange={(event) => updateOverride(index, { summaryMaxOutputTokens: event.target.value === "" ? undefined : Number(event.target.value) })} />
            <button type="button" className="secondary-btn" onClick={() => removeOverride(index)} aria-label={`Remove override ${index + 1}`}>✕</button>
          </div>
        ))}
        <button type="button" className="secondary-btn" onClick={addOverride}>+ Add override</button>
      </div>
      <button type="submit">Create version</button>
    </form>
    <div className="settings-list">{policies.filter(policy => policy.kind === "compaction").map(policy => <div className="settings-row" key={policy.id}>
      <div><strong>{policy.name}</strong><span>v{policy.version} · {String(policy.config.mode ?? "configured")}</span></div>
      <button className="secondary-btn" disabled={policy.status !== "active"} onClick={() => onDefault(policy.id)}>Use as default</button>
    </div>)}</div>
  </section>;
}

function StandingApprovalsSection({ sessionId }: { sessionId: string | null }) {
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
    try {
      await apiFetch(`/v1/sessions/${encodeURIComponent(sessionId)}/standing-approvals/${encodeURIComponent(ruleId)}/revoke`, { method: "POST" });
      setRules(prev => prev.filter(r => r.id !== ruleId));
      setLoadError(null);
    } catch (err) {
      setLoadError((err as Error).message);
    }
  }, [sessionId]);

  if (!sessionId) return null;

  return <section className="settings-subsection standing-approvals-section">
    <header><h3>Standing approvals</h3>
      <p>Tools automatically authorised for this session. Revoke to require approval again.</p></header>
    {loading && <div className="settings-empty">Loading…</div>}
    {!loading && error && <div className="settings-error">{error}</div>}
    {!loading && !error && visibleRules.length === 0 &&
      <div className="settings-empty">No standing approvals for this session.</div>}
    {!loading && visibleRules.length > 0 && <div className="settings-list">
      {visibleRules.map(rule => <div className="settings-row" key={rule.id}>
        <div><strong>{rule.toolName}</strong><span>{rule.scopeDisplay}</span></div>
        <button className="secondary-btn" onClick={() => revoke(rule.id)}>Revoke</button>
      </div>)}
    </div>}
  </section>;
}
