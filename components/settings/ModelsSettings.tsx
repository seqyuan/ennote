"use client";

import type { FormEvent } from "react";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

export function ModelsSettings({ providers, models, refresh, setError }: {
  providers: ProviderProfile[];
  models: ModelProfile[];
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  async function createModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    try {
      await apiFetch("/v1/model-profiles", { method: "POST", body: JSON.stringify({
        providerId: data.get("providerId"), modelName: data.get("modelName"), displayName: data.get("displayName"),
        contextWindow: Number(data.get("contextWindow")), maxOutputTokens: Number(data.get("maxOutputTokens")),
        supportsVision: data.get("supportsVision") === "on", supportsToolUse: data.get("supportsToolUse") === "on",
        supportsThinking: data.get("supportsThinking") === "on", isDefault: data.get("isDefault") === "on",
      }) });
      form.reset();
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  async function setDefault(modelId: string) {
    try {
      await apiFetch(`/v1/model-profiles/${encodeURIComponent(modelId)}/default`, { method: "PUT" });
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  return <section className="settings-tab-section" aria-labelledby="settings-models-heading">
    <header><h2 id="settings-models-heading">Models</h2>
      <p>Wire model IDs, context limits, capabilities, and global default selection.</p></header>
    <form className="settings-form model-form" onSubmit={createModel}>
      <label>Provider<select name="providerId" required defaultValue=""><option value="" disabled>Select provider</option>
        {providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}</select></label>
      <label>API model ID<input name="modelName" required /></label>
      <label>Display name<input name="displayName" /></label>
      <label>Context window<input name="contextWindow" type="number" min="1" required defaultValue="128000" /></label>
      <label>Max output<input name="maxOutputTokens" type="number" min="1" required defaultValue="8192" /></label>
      <div className="settings-toggles">
        <label><input name="supportsToolUse" type="checkbox" defaultChecked /> Tool use</label>
        <label><input name="supportsThinking" type="checkbox" /> Thinking</label>
        <label><input name="supportsVision" type="checkbox" /> Vision</label>
        <label><input name="isDefault" type="checkbox" /> Global default</label>
      </div>
      <button type="submit" disabled={providers.length === 0}>Add model</button>
    </form>
    <div className="settings-list">{models.map(model => <div className="settings-row" key={model.id}>
      <div><strong>{model.displayName || model.modelName}</strong>
        <span>{providers.find(provider => provider.id === model.providerId)?.name ?? "Missing provider"} · {model.modelName} · {model.contextWindow.toLocaleString()} ctx</span></div>
      <button className="secondary-btn" disabled={model.isDefault} onClick={() => setDefault(model.id)}>
        {model.isDefault ? "Default" : "Make default"}
      </button>
    </div>)}</div>
  </section>;
}
