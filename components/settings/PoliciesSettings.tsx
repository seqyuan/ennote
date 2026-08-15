"use client";

import { Archive } from "lucide-react";
import type { FormEvent } from "react";
import type { PolicyProfile } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

const policySections: Array<{ kind: PolicyProfile["kind"]; title: string; description: string; defaultConfig: string }> = [
  { kind: "tool", title: "Tool policy", description: "Permission mode and allowed local execution boundaries.", defaultConfig: '{"mode":"restricted","allowedTools":["read","ls","grep","find"],"allowedExecutables":["git","rg"],"maxTimeoutSeconds":300}' },
  { kind: "turn", title: "Turn routing", description: "Model routing candidates and upgrade thresholds.", defaultConfig: '{"mode":"context_upgrade","threshold":0.7,"candidateModelProfileIds":[]}' },
  { kind: "vision", title: "Vision", description: "Native image handling and descriptor fallback boundaries.", defaultConfig: '{"mode":"reject","maxImageBytes":10485760,"maxPixels":40000000}' },
];

export function PoliciesSettings({ policies, refresh, setError }: {
  policies: PolicyProfile[];
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  async function createPolicy(event: FormEvent<HTMLFormElement>, kind: PolicyProfile["kind"]) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    try {
      const config = JSON.parse(String(data.get("config") || "{}"));
      await apiFetch("/v1/policy-profiles", { method: "POST", body: JSON.stringify({ name: data.get("name"), kind, config }) });
      form.reset();
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

  async function deactivate(policy: PolicyProfile) {
    if (!window.confirm(`Deactivate policy “${policy.name}” v${policy.version}?`)) return;
    try {
      await apiFetch(`/v1/policy-profiles/${encodeURIComponent(policy.id)}`, { method: "DELETE" });
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  return <section className="settings-tab-section" aria-labelledby="settings-policies-heading">
    <header><h2 id="settings-policies-heading">Policies</h2>
      <p>Immutable versioned execution, routing, and image handling configuration.</p></header>
    {policySections.map(section => <PolicyEditor key={section.kind} {...section} policies={policies}
      onSubmit={createPolicy} onDefault={setDefault} onDeactivate={deactivate} />)}
  </section>;
}

function PolicyEditor({ kind, title, description, policies, defaultConfig, onSubmit, onDefault, onDeactivate }: {
  kind: PolicyProfile["kind"];
  title: string;
  description: string;
  policies: PolicyProfile[];
  defaultConfig: string;
  onSubmit: (event: FormEvent<HTMLFormElement>, kind: PolicyProfile["kind"]) => Promise<void>;
  onDefault: (policyId: string) => Promise<void>;
  onDeactivate: (policy: PolicyProfile) => Promise<void>;
}) {
  return <section className="settings-subsection">
    <header><h3>{title}</h3><p>{description}</p></header>
    <form className="settings-form policy-form" onSubmit={event => onSubmit(event, kind)}>
      <label>Name<input name="name" required /></label>
      <label>Configuration JSON<textarea name="config" required defaultValue={defaultConfig} /></label>
      <button type="submit">Create version</button>
    </form>
    <div className="settings-list">{policies.filter(policy => policy.kind === kind).map(policy => <div className="settings-row" key={policy.id}>
      <div><strong>{policy.name}</strong><span>v{policy.version} · {String(policy.config.mode ?? "configured")}</span></div>
      <button className="secondary-btn" disabled={policy.status !== "active"} onClick={() => onDefault(policy.id)}>Use as default</button>
      <button className="secondary-btn" type="button" disabled={policy.status !== "active"}
        title="Deactivate policy" aria-label={`Deactivate policy ${policy.name} version ${policy.version}`}
        onClick={() => onDeactivate(policy)}><Archive size={14} aria-hidden="true" /></button>
    </div>)}</div>
  </section>;
}
