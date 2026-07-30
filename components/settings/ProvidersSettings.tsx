"use client";

import type { FormEvent } from "react";
import { ProviderSettingsRow } from "@/components/settings/ProviderSettingsRow";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

export function ProvidersSettings({ providers, models, refresh, setError }: {
  providers: ProviderProfile[];
  models: ModelProfile[];
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  async function createProvider(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    try {
      await apiFetch("/v1/provider-profiles", { method: "POST", body: JSON.stringify({
        name: data.get("name"), providerType: "openai-compatible", baseUrl: data.get("baseUrl"),
        credentialRef: data.get("credentialRef"),
      }) });
      form.reset();
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  return <section className="settings-tab-section" aria-labelledby="settings-providers-heading">
    <header><h2 id="settings-providers-heading">Providers</h2>
      <p>Connection profiles and safe credential references used by models.</p></header>
    <form className="settings-form provider-form" onSubmit={createProvider}>
      <label>Name<input name="name" required /></label>
      <label>Base URL<input name="baseUrl" type="url" required placeholder="https://api.example.com" /></label>
      <label>Credential reference<input name="credentialRef" required placeholder="env:PROVIDER_API_KEY" /></label>
      <button type="submit">Add provider</button>
    </form>
    <div className="settings-list">{providers.map(provider => <ProviderSettingsRow key={provider.id} provider={provider}
      models={models.filter(model => model.providerId === provider.id)} />)}</div>
  </section>;
}
