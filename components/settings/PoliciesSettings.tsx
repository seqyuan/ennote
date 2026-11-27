"use client";

import { Archive } from "lucide-react";
import { useState, type FormEvent } from "react";
import type { PolicyProfile } from "@/components/settings/types";
import { useT } from "@/components/LocaleProvider";
import { confirmAction } from "@/components/ui/confirm";
import { useSettingsStatus } from "@/components/settings/SettingsStatus";
import { SettingsEmpty, SettingsList, SettingsRow, SettingsSection } from "@/components/settings/SettingsPrimitives";
import { apiFetch } from "@/lib/worker-api.client";

const policySections: Array<{ kind: PolicyProfile["kind"]; titleKey: string; descriptionKey: string; defaultConfig: string }> = [
  { kind: "tool", titleKey: "settings.policies.tool.title", descriptionKey: "settings.policies.tool.description", defaultConfig: '{"mode":"restricted","allowedTools":["read","ls","grep","find"],"allowedExecutables":["git","rg"],"maxTimeoutSeconds":300}' },
  { kind: "turn", titleKey: "settings.policies.turn.title", descriptionKey: "settings.policies.turn.description", defaultConfig: '{"mode":"context_upgrade","threshold":0.7,"candidateModelProfileIds":[]}' },
  { kind: "vision", titleKey: "settings.policies.vision.title", descriptionKey: "settings.policies.vision.description", defaultConfig: '{"mode":"reject","maxImageBytes":10485760,"maxPixels":40000000}' },
];

/** A config error as a message key rather than a sentence, so the same check
 *  reads in either locale and the unit test asserts the rule, not the English. */
export type PolicyConfigError =
  | { key: "settings.policies.error.invalidJson"; params: { detail: string } }
  | { key: "settings.policies.error.notObject"; params?: undefined };

/** A policy config is a JSON object. Validation is shared by the textarea's
 *  blur and by submit so both report the same message for the same text. */
export function validatePolicyConfig(raw: string): PolicyConfigError | null {
  const text = raw.trim();
  if (text === "") return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (reason) {
    return { key: "settings.policies.error.invalidJson", params: { detail: (reason as Error).message } };
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { key: "settings.policies.error.notObject" };
  }
  return null;
}

export function PoliciesSettings({ policies, refresh, setError }: {
  policies: PolicyProfile[];
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const { markSaving, markSaved } = useSettingsStatus();
  const t = useT();
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
    <header><h2 id="settings-policies-heading">{t("settings.policies.title")}</h2>
      <p>{t("settings.policies.desc")}</p></header>
    {policySections.map(section => <PolicyEditor key={section.kind} {...section} title={t(section.titleKey)} description={t(section.descriptionKey)} policies={policies}
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
  const [configError, setConfigError] = useState<PolicyConfigError | null>(null);
  const t = useT();
  const versions = policies.filter(policy => policy.kind === kind);

  // Validate where the user types; submit re-reads the same field so a request
  // never carries a config the form already knows is unparseable.
  function checkConfig(form: HTMLFormElement): boolean {
    const error = validatePolicyConfig(String(new FormData(form).get("config") ?? ""));
    setConfigError(error);
    return error === null;
  }

  return <SettingsSection title={title} description={description}>
    <form className="settings-form policy-form" onSubmit={(event) => {
      if (!checkConfig(event.currentTarget)) { event.preventDefault(); return; }
      void onSubmit(event, kind);
    }}>
      <label>{t("settings.policies.name")}<input name="name" required /></label>
      <label>{t("settings.policies.config")}<textarea name="config" required defaultValue={defaultConfig}
        aria-invalid={configError ? true : undefined}
        onChange={() => { if (configError) setConfigError(null); }}
        onBlur={(event) => setConfigError(validatePolicyConfig(event.target.value))} /></label>
      {configError && <p className="settings-field-error" role="alert">{t(configError.key, configError.params)}</p>}
      <button type="submit">{t("settings.policies.createVersion")}</button>
    </form>
    <SettingsList>
      {versions.length === 0 && <SettingsEmpty>{t("settings.policies.empty")}</SettingsEmpty>}
      {versions.map(policy => <SettingsRow
        key={policy.id}
        title={policy.name}
        meta={`v${policy.version} · ${String(policy.config.mode ?? "configured")}`}
        actions={<>
          <button className="secondary-btn" disabled={policy.status !== "active"} onClick={() => onDefault(policy.id)}>{t("settings.policies.useAsDefault")}</button>
          <button className="secondary-btn" type="button" disabled={policy.status !== "active"}
            title={t("settings.policies.deactivate")} aria-label={t("settings.policies.deactivateAria", { name: policy.name, version: String(policy.version) })}
            onClick={() => onDeactivate(policy)}><Archive size={14} aria-hidden="true" /></button>
        </>}
      />)}
    </SettingsList>
  </SettingsSection>;
}
