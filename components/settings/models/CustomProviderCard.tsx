"use client";

import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { SecretTextInput } from "@/components/settings/SecretTextInput";
import { apiKeyFailure } from "@/lib/api-key";
import { createProviderWithModels } from "@/lib/provider-create";
import { validateModels, type ModelDraft } from "@/lib/model-draft";
import { ModelListEditor } from "@/components/settings/models/ModelListEditor";

/** A route id usable as a settings key AND as a credential id: starts with a
 *  lowercase letter, then lowercase letters/digits/dashes. */
const ROUTE_PATTERN = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;

/** The provider types a hand-declared route may name (mapped to a wire API by
 *  the backend). */
const PROTOCOLS = ["openai-compatible", "anthropic"] as const;

/**
 * The card that declares a provider the built-in catalog does not ship — an
 * OpenAI-compatible gateway, a self-hosted server, or a provider newer than the
 * catalog. It is its own card (not the editor with extra fields) because the
 * route id is being chosen here and the settings address does not exist until
 * the create lands.
 */
export function CustomProviderCard({ taken, onClose, refresh, setError, onSaved }: {
  taken: readonly string[];
  onClose: (changed: boolean) => void;
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
  onSaved?: (name: string) => void;
}) {
  const t = useT();
  const [route, setRoute] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [protocol, setProtocol] = useState<string>(PROTOCOLS[0]);
  const [apiKey, setApiKey] = useState("");
  const [models, setModels] = useState<readonly ModelDraft[]>([]);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | undefined>(undefined);

  const routeInvalid = route.length > 0 && !ROUTE_PATTERN.test(route);
  const routeTaken = taken.includes(route);
  const modelFailure = validateModels(models);
  const keyFailure = apiKeyFailure(apiKey);
  const keyValue = apiKey.trim();
  const ready = route.length > 0 && !routeInvalid && !routeTaken
    && baseURL.trim().length > 0 && models.length > 0 && modelFailure === undefined
    && keyFailure === undefined;

  let hint: string | undefined;
  if (failure === undefined && !ready && !routeInvalid && !routeTaken && keyFailure === undefined) {
    if (route.length === 0) hint = t("settings.models.customRouteHint");
    else if (baseURL.trim().length === 0) hint = t("settings.models.customNeedsBaseUrl");
    else if (models.length === 0) hint = t("settings.models.customNeedsModels");
    else if (modelFailure !== undefined) hint = `${t("settings.models.model")} ${modelFailure.index + 1}: ${t(`settings.models.${modelFailure.key}`)}`;
  }

  async function create(): Promise<void> {
    setBusy(true);
    setFailure(undefined);
    try {
      await createProviderWithModels({
        key: route,
        name: displayName.trim() || route,
        providerType: protocol,
        baseUrl: baseURL.trim(),
        apiKey: keyValue || undefined,
        models,
      });
      setError(null);
      onSaved?.(displayName.trim() || route);
      await refresh();
      onClose(true);
    } catch (reason) {
      setFailure((reason as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="settings-row provider-settings-row settings-models-editor">
      <div className="settings-models-editor-header">
        <span className="settings-models-editor-title">{t("settings.models.customTitle")}</span>
      </div>
      <div className="settings-models-field">
        <span className="settings-models-fieldlabel">{t("settings.models.customRoute")}</span>
        <input
          className="settings-models-input"
          type="text"
          value={route}
          placeholder="acme-gateway"
          disabled={busy}
          aria-label={t("settings.models.customRoute")}
          onChange={(e) => { setRoute(e.target.value); }}
        />
        {routeInvalid || routeTaken
          ? <p className="settings-models-error">{t(routeInvalid ? "settings.models.customRouteInvalid" : "settings.models.customRouteTaken")}</p>
          : <p className="settings-models-hint">{t("settings.models.customRouteHint")}</p>}
      </div>
      <div className="settings-models-field">
        <span className="settings-models-fieldlabel">{t("settings.models.customDisplayName")}</span>
        <input
          className="settings-models-input"
          type="text"
          value={displayName}
          placeholder={route.length === 0 ? t("settings.models.customDisplayName") : route}
          disabled={busy}
          aria-label={t("settings.models.customDisplayName")}
          onChange={(e) => { setDisplayName(e.target.value); }}
        />
      </div>
      <div className="settings-models-field">
        <span className="settings-models-fieldlabel">{t("settings.models.baseUrl")}</span>
        <input
          className="settings-models-input"
          type="text"
          value={baseURL}
          placeholder="https://gateway.example/v1"
          disabled={busy}
          aria-label={t("settings.models.baseUrl")}
          onChange={(e) => { setBaseURL(e.target.value); }}
        />
      </div>
      <div className="settings-models-field">
        <span className="settings-models-fieldlabel">{t("settings.models.customApi")}</span>
        <select
          className="settings-models-input settings-models-select"
          value={protocol}
          disabled={busy}
          aria-label={t("settings.models.customApi")}
          onChange={(e) => { setProtocol(e.target.value); }}
        >
          {PROTOCOLS.map((choice) => <option key={choice} value={choice}>{choice}</option>)}
        </select>
      </div>
      <div className="settings-models-field">
        <span className="settings-models-fieldlabel">{t("settings.models.keyInput")}</span>
        <SecretTextInput value={apiKey} onChange={setApiKey} placeholder={t("settings.models.keyPlaceholder")} ariaLabel={t("settings.models.keyInput")} />
        {keyFailure === undefined ? null
          : <p className="settings-models-error">{t(`settings.models.${keyFailure}`)}</p>}
      </div>
      <ModelListEditor
        models={models}
        onChange={setModels}
        disabled={busy}
        probe={{ baseUrl: baseURL.trim(), apiKey: keyValue }}
        probeBlocked={keyFailure !== undefined ? t(`settings.models.${keyFailure}`) : undefined}
        onFetchError={setFailure}
      />
      {failure !== undefined ? <p className="settings-models-error">{failure}</p> : null}
      {hint === undefined ? null : <p className="settings-models-hint">{hint}</p>}
      <div className="settings-models-editor-actions">
        <button type="button" className="secondary-btn" disabled={busy} onClick={() => onClose(false)}>
          {t("settings.models.cancel")}
        </button>
        <button type="button" className="settings-models-primary" disabled={busy || !ready} onClick={() => { void create(); }}>
          {busy ? t("settings.models.creating") : t("settings.models.create")}
        </button>
      </div>
    </div>
  );
}
