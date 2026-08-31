"use client";

import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { SecretTextInput } from "@/components/settings/SecretTextInput";
import { ModelListEditor } from "@/components/settings/models/ModelListEditor";
import { apiFetch } from "@/lib/worker-api.client";
import { apiKeyFailure } from "@/lib/api-key";
import { createProviderWithModels } from "@/lib/provider-create";
import { validateModels, type ModelDraft } from "@/lib/model-draft";
import { planModelSync, type BeforeModel } from "@/lib/model-sync";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";

function toDrafts(models: readonly ModelProfile[]): ModelDraft[] {
  return models.map(model => ({
    id: model.modelName,
    ...(model.displayName && model.displayName !== model.modelName ? { name: model.displayName } : {}),
    contextWindow: model.contextWindow,
    maxTokens: model.maxOutputTokens,
  }));
}

async function applySync(plan: ReturnType<typeof planModelSync>): Promise<void> {
  for (const id of plan.toDelete) {
    await apiFetch(`/v1/model-profiles/${encodeURIComponent(id)}`, { method: "DELETE" });
  }
  for (const { id, input } of plan.toUpdate) {
    await apiFetch(`/v1/model-profiles/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(input) });
  }
  for (const input of plan.toCreate) {
    await apiFetch("/v1/model-profiles", { method: "POST", body: JSON.stringify(input) });
  }
}

/**
 * The editor card for one provider. Pixel-aligned with dsh ProviderEditor:
 * API key field, a 自定义设置 fold (base URL, optional display name, model
 * catalog), and a Cancel/Apply footer. Reasoning effort is deliberately
 * absent — it is a per-model capability offered by the composer picker.
 */
export function ProviderEditor({ provider, models, creating, hideTitle, onClose, refresh, setError, onSaved }: {
  provider: ProviderProfile;
  models: readonly ModelProfile[];
  creating?: boolean;
  hideTitle?: boolean;
  onClose: (changed: boolean) => void;
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
  onSaved?: (name: string) => void;
}) {
  const t = useT();
  const [keyDraft, setKeyDraft] = useState("");
  const [baseURL, setBaseURL] = useState(provider.baseUrl ?? "");
  const [displayName, setDisplayName] = useState(provider.name ?? "");
  const [drafts, setDrafts] = useState<readonly ModelDraft[]>(() => creating ? [] : toDrafts(models));
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | undefined>(undefined);

  const keyFailure = apiKeyFailure(keyDraft);
  const modelFailure = validateModels(drafts);
  const keyValue = keyDraft.trim();
  const before: BeforeModel[] = models.map(model => ({
    id: model.id,
    modelName: model.modelName,
    displayName: model.displayName,
    contextWindow: model.contextWindow,
    maxOutputTokens: model.maxOutputTokens,
  }));

  async function apply(): Promise<void> {
    setBusy(true);
    setFailure(undefined);
    try {
      if (creating) {
        await createProviderWithModels({
          key: provider.id,
          name: displayName.trim() || provider.name,
          providerType: provider.providerType,
          baseUrl: baseURL.trim() || provider.baseUrl,
          apiKey: keyValue || undefined,
          models: drafts,
        });
      } else {
        await apiFetch(`/v1/provider-profiles/${encodeURIComponent(provider.id)}`, {
          method: "PUT",
          body: JSON.stringify({
            name: displayName.trim() || undefined,
            baseUrl: baseURL.trim() || undefined,
            apiKey: keyValue || undefined,
          }),
        });
        const plan = planModelSync(provider.id, before, drafts);
        await applySync(plan);
      }
      setError(null);
      onSaved?.(displayName.trim() || provider.name);
      await refresh();
      onClose(true);
    } catch (reason) {
      setFailure((reason as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const submitDisabled = busy || keyFailure !== undefined || modelFailure !== undefined;
  const keyPlaceholder = provider.credentialConfigured
    ? t("settings.models.keyStored")
    : t("settings.models.keyPlaceholder");

  return (
    <div className="settings-row provider-settings-row settings-models-editor">
      {!hideTitle && (
        <div className="settings-models-editor-header">
          <span className="settings-models-editor-title">{provider.name}</span>
          {provider.name !== provider.id && (
            <span className="settings-models-editor-route">{provider.id}</span>
          )}
        </div>
      )}
      <div className="settings-models-field">
        <span className="settings-models-fieldlabel">{t("settings.models.keyInput")}</span>
        <SecretTextInput value={keyDraft} onChange={setKeyDraft} placeholder={keyPlaceholder} ariaLabel={t("settings.models.keyInput")} />
        {keyFailure === undefined ? null
          : <p className="settings-models-error">{t(`settings.models.${keyFailure}`)}</p>}
      </div>
      <details className="settings-models-customized">
        <summary className="settings-models-customized-summary">{t("settings.models.customized")}</summary>
        <div className="settings-models-customized-body">
          {provider.custom && (
            <div className="settings-models-field">
              <span className="settings-models-fieldlabel">{t("settings.models.customDisplayName")}</span>
              <input
                className="settings-models-input"
                type="text"
                value={displayName}
                disabled={busy}
                aria-label={t("settings.models.customDisplayName")}
                onChange={(e) => { setDisplayName(e.target.value); }}
              />
            </div>
          )}
          <div className="settings-models-field">
            <span className="settings-models-fieldlabel">{t("settings.models.baseUrl")}</span>
            <input
              className="settings-models-input"
              type="text"
              value={baseURL}
              disabled={busy}
              placeholder={provider.baseUrl || t("settings.models.baseUrlDefault")}
              aria-label={t("settings.models.baseUrl")}
              onChange={(e) => { setBaseURL(e.target.value); }}
            />
          </div>
          <ModelListEditor
            models={drafts}
            onChange={setDrafts}
            disabled={busy}
            probe={{ baseUrl: baseURL.trim(), apiKey: keyValue }}
            probeBlocked={keyFailure !== undefined ? t(`settings.models.${keyFailure}`) : undefined}
            onFetchError={setFailure}
          />
        </div>
      </details>
      {failure !== undefined ? <p className="settings-models-error">{failure}</p> : null}
      <div className="settings-models-editor-actions">
        <button type="button" className="secondary-btn" disabled={busy} onClick={() => onClose(false)}>
          {t("settings.models.cancel")}
        </button>
        <button type="button" className="settings-models-primary" disabled={submitDisabled} onClick={() => { void apply(); }}>
          {busy
            ? (creating ? t("settings.models.creating") : t("settings.models.applying"))
            : (creating ? t("settings.models.create") : t("settings.models.apply"))}
        </button>
      </div>
    </div>
  );
}
