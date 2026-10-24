"use client";

import { Archive } from "lucide-react";
import type { FormEvent } from "react";
import type { PolicyProfile } from "@/components/settings/types";
import { confirmAction } from "@/components/ui/confirm";
import { useSettingsStatus } from "@/components/settings/SettingsStatus";
import { SettingsList, SettingsRow, SettingsSection } from "@/components/settings/SettingsPrimitives";
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
  const { markSaving, markSaved } = useSettingsStatus();
  async function createPolicy(event: FormEvent<HTMLFormElement>, kind: PolicyProfile["kind"]) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    try {
      const config = JSON.parse(String(data.get("config") || "{}"));
      markSaving();
      await apiFetch("/v1/policy-profiles", { method: "POST", body: JSON.stringify({ name: data.get("name"), kind, config }) });
      form.reset();
      setError(null);
      await refresh();
      markSaved();
    } catch (reason) {
      setError((reason as Error).message);
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

  async function deactivate(policy: PolicyProfile) {
    if (!(await confirmAction({
      title: `Deactivate policy “${policy.name}” v${policy.version}?`,
      description: "Existing Runs keep their frozen policy. New Runs will no longer select it as the default.",
      confirmLabel: "Deactivate",
      tone: "danger",
    }))) return;
    markSaving();
    try {
      await apiFetch(`/v1/policy-profiles/${encodeURIComponent(policy.id)}`, { method: "DELETE" });
      setError(null);
      await refresh();
      markSaved();
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
  return <SettingsSection title={title} description={description}>
    <form className="settings-form policy-form" onSubmit={event => onSubmit(event, kind)}>
      <label>Name<input name="name" required /></label>
      <label>Configuration JSON<textarea name="config" required defaultValue={defaultConfig} /></label>
      <button type="submit">Create version</button>
    </form>
    <SettingsList>
      {policies.filter(policy => policy.kind === kind).map(policy => <SettingsRow
        key={policy.id}
        title={policy.name}
        meta={`v${policy.version} · ${String(policy.config.mode ?? "configured")}`}
        actions={<>
          <button className="secondary-btn" disabled={policy.status !== "active"} onClick={() => onDefault(policy.id)}>Use as default</button>
          <button className="secondary-btn" type="button" disabled={policy.status !== "active"}
            title="Deactivate policy" aria-label={`Deactivate policy ${policy.name} version ${policy.version}`}
            onClick={() => onDeactivate(policy)}><Archive size={14} aria-hidden="true" /></button>
        </>}
      />)}
    </SettingsList>
  </SettingsSection>;
}
