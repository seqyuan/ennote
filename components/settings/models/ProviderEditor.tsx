"use client";

import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { SecretTextInput } from "@/components/settings/SecretTextInput";
import { FetchModelsButton } from "@/components/settings/models/FetchModelsButton";
import { ModelListEditor } from "@/components/settings/models/ModelListEditor";
import { apiFetch } from "@/lib/worker-api.client";
import { apiKeyFailure } from "@/lib/api-key";
import { createProviderWithModels } from "@/lib/provider-create";
import { validateModels, type ModelDraft } from "@/lib/model-draft";
import { planModelSync, type BeforeModel } from "@/lib/model-sync";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";

const inputStyle: React.CSSProperties = {
  height: 32, padding: "0 10px", border: "1px solid var(--stg-border-l2)",
  borderRadius: 8, background: "var(--stg-input-fill)", color: "var(--stg-text-primary)",
  font: "14px/22px var(--font-sans)", minWidth: 0,
};

/** Existing models converted to editable drafts (structurally open). */
function toDrafts(models: readonly ModelProfile[]): ModelDraft[] {
  return models.map(model => ({
    id: model.modelName,
    ...(model.displayName && model.displayName !== model.modelName ? { name: model.displayName } : {}),
    contextWindow: model.contextWindow,
    maxTokens: model.maxOutputTokens,
  }));
}

function draftId(draft: ModelDraft): string {
  return (typeof draft.id === "string" ? draft.id : "").trim();
}

/** Apply a model-sync plan through the wire. */
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
 * The editor card for one provider. In update mode it edits an existing
 * profile: a single write-only API key field (blank keeps the stored key), a
 * collapsed 自定义设置 fold with the curated per-family extras (base URL, and
 * the display name of a hand-declared route), and the model list editor with
 * a "Fetch available models" interrogation that adopts the provider's catalog
 * into the draft. In create mode (directory setup/adopt) the card writes the
 * profile through POST and leaves a blank model list to the built-in catalog.
 */
export function ProviderEditor({ provider, models, creating, hideTitle, onClose, refresh, setError, onSaved }: {
  provider: ProviderProfile;
  models: readonly ModelProfile[];
  /** Create the profile instead of updating it (directory setup/adopt card). */
  creating?: boolean;
  /** Hide the title row (the adopt card renders its own provider select). */
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
            // Blank keeps the stored key; a typed key replaces it.
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

  return (
    <div className="settings-row provider-settings-row" style={{ flexDirection: "column", gap: 12 }}>
      {!hideTitle && (
        <div style={{ display: "flex", alignItems: "baseline", gap: 8 }}>
          <strong style={{ fontSize: 14, fontWeight: 500 }}>{provider.name}</strong>
          {provider.name !== provider.id && <span style={{ fontSize: 11, color: "var(--stg-text-tertiary)" }}>{provider.id}</span>}
        </div>
      )}
      <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 12, color: "var(--stg-text-secondary)" }}>
        {t("settings.models.keyInput")}
        <SecretTextInput value={keyDraft} onChange={setKeyDraft}
          placeholder={provider.credentialConfigured ? t("settings.models.keyStored") : t("settings.models.keyPlaceholder")} />
        {keyFailure === undefined ? null
          : <span style={{ fontSize: 11, color: "var(--stg-danger)" }}>{t(`settings.models.${keyFailure}`)}</span>}
      </label>
      <details style={{ fontSize: 12 }}>
        <summary style={{ cursor: "pointer", color: "var(--stg-text-secondary)", fontWeight: 500, fontSize: 12 }}>
          {t("settings.models.customized")}
        </summary>
        <div style={{ display: "flex", flexDirection: "column", gap: 10, paddingTop: 10 }}>
          {provider.custom && (
            <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 12, color: "var(--stg-text-secondary)" }}>
              {t("settings.models.customDisplayName")}
              <input type="text" value={displayName} disabled={busy} style={inputStyle}
                aria-label={t("settings.models.customDisplayName")}
                onChange={(e) => { setDisplayName(e.target.value); }} />
            </label>
          )}
          <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 12, color: "var(--stg-text-secondary)" }}>
            {t("settings.models.baseUrl")}
            <input type="text" value={baseURL} disabled={busy} style={inputStyle}
              aria-label={t("settings.models.baseUrl")}
              onChange={(e) => { setBaseURL(e.target.value); }} />
          </label>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span style={{ fontSize: 12, fontWeight: 500, color: "var(--stg-text-primary)" }}>{t("settings.models.models")}</span>
            {creating && drafts.length === 0 && (
              <span style={{ fontSize: 11, color: "var(--stg-text-tertiary)" }}>{t("settings.models.modelsInherited")}</span>
            )}
            <FetchModelsButton probe={{ baseUrl: baseURL.trim(), apiKey: keyValue }}
              existingIds={drafts.map(draftId)}
              onAdopt={(selected) => {
                const byId = new Map(drafts.map(d => [draftId(d), d]));
                for (const s of selected) byId.set(draftId(s), byId.get(draftId(s)) ?? s);
                setDrafts([...byId.values()]);
              }}
              onError={setFailure}
              disabled={busy} />
          </div>
          <ModelListEditor models={drafts} onChange={setDrafts} disabled={busy} />
        </div>
      </details>
      {failure !== undefined ? <span style={{ fontSize: 12, color: "var(--stg-danger)" }}>{failure}</span> : null}
      <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
        <button type="button" className="secondary-btn" disabled={busy} onClick={() => onClose(false)}>{t("settings.models.cancel")}</button>
        <button type="button" disabled={submitDisabled}
          style={{ minHeight: 36, padding: "0 14px", border: "none", borderRadius: 18, background: "var(--stg-brand)", color: "#fff", fontWeight: 500, cursor: "pointer", opacity: submitDisabled ? 0.5 : 1 }}
          onClick={() => { void apply(); }}>
          {busy
            ? (creating ? t("settings.models.creating") : t("settings.models.applying"))
            : (creating ? t("settings.models.create") : t("settings.models.apply"))}
        </button>
      </div>
    </div>
  );
}
