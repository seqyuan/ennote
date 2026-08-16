"use client";

import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { SecretTextInput } from "@/components/settings/SecretTextInput";
import { FetchModelsButton } from "@/components/settings/models/FetchModelsButton";
import { apiFetch } from "@/lib/worker-api.client";
import { apiKeyFailure } from "@/lib/api-key";
import { validateModels, type ModelDraft } from "@/lib/model-draft";
import { ModelListEditor } from "@/components/settings/models/ModelListEditor";

/** A route id usable as a settings key AND as a credential id: starts with a
 *  lowercase letter, then lowercase letters/digits/dashes. */
const ROUTE_PATTERN = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;

/** The provider types a hand-declared route may name (mapped to a wire API by
 *  the backend). */
const PROTOCOLS = ["openai-compatible", "anthropic"] as const;

const inputStyle: React.CSSProperties = {
  height: 32, padding: "0 10px", border: "1px solid var(--stg-border-l2)",
  borderRadius: 8, background: "var(--stg-input-fill)", color: "var(--stg-text-primary)",
  font: "14px/22px var(--font-sans)", minWidth: 0,
};

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

  // The gate line names the field the user is still looking at, and stays
  // silent once the card is satisfied.
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
      await apiFetch("/v1/provider-profiles", { method: "POST", body: JSON.stringify({
        key: route,
        name: displayName.trim() || route,
        providerType: protocol,
        baseUrl: baseURL.trim(),
        apiKey: keyValue || undefined,
      }) });
      for (const model of models) {
        await apiFetch("/v1/model-profiles", { method: "POST", body: JSON.stringify({
          providerId: route,
          modelName: model.id,
          ...(typeof model.name === "string" && model.name.length > 0 ? { displayName: model.name } : {}),
          // A hand-declared route has no catalog fallback, so a blank capacity
          // falls back to common defaults rather than failing the write.
          contextWindow: typeof model.contextWindow === "number" ? model.contextWindow : 131072,
          maxOutputTokens: typeof model.maxTokens === "number" ? model.maxTokens : 16384,
          supportsToolUse: true,
        }) });
      }
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
    <div className="settings-row provider-settings-row" style={{ flexDirection: "column", gap: 12 }}>
      <div style={{ display: "flex", alignItems: "baseline", gap: 8 }}>
        <strong style={{ fontSize: 14, fontWeight: 500 }}>{t("settings.models.customTitle")}</strong>
      </div>
      <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 12, color: "var(--stg-text-secondary)" }}>
        {t("settings.models.customRoute")}
        <input type="text" value={route} placeholder="acme-gateway" disabled={busy} style={inputStyle}
          aria-label={t("settings.models.customRoute")}
          onChange={(e) => { setRoute(e.target.value); }} />
        {routeInvalid || routeTaken
          ? <span style={{ fontSize: 11, color: "var(--stg-danger)" }}>{t(routeInvalid ? "settings.models.customRouteInvalid" : "settings.models.customRouteTaken")}</span>
          : <span style={{ fontSize: 11, color: "var(--stg-text-tertiary)" }}>{t("settings.models.customRouteHint")}</span>}
      </label>
      <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 12, color: "var(--stg-text-secondary)" }}>
        {t("settings.models.customDisplayName")}
        <input type="text" value={displayName} placeholder={route.length === 0 ? t("settings.models.customDisplayName") : route} disabled={busy} style={inputStyle}
          aria-label={t("settings.models.customDisplayName")}
          onChange={(e) => { setDisplayName(e.target.value); }} />
      </label>
      <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 12, color: "var(--stg-text-secondary)" }}>
        {t("settings.models.baseUrl")}
        <input type="text" value={baseURL} placeholder="https://gateway.example/v1" disabled={busy} style={inputStyle}
          aria-label={t("settings.models.baseUrl")}
          onChange={(e) => { setBaseURL(e.target.value); }} />
      </label>
      <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 12, color: "var(--stg-text-secondary)" }}>
        {t("settings.models.customApi")}
        <select value={protocol} disabled={busy} style={inputStyle} aria-label={t("settings.models.customApi")}
          onChange={(e) => { setProtocol(e.target.value); }}>
          {PROTOCOLS.map((choice) => <option key={choice} value={choice}>{choice}</option>)}
        </select>
      </label>
      <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 12, color: "var(--stg-text-secondary)" }}>
        {t("settings.models.keyInput")}
        <SecretTextInput value={apiKey} onChange={setApiKey} placeholder={t("settings.models.keyPlaceholder")} />
        {keyFailure === undefined ? null
          : <span style={{ fontSize: 11, color: "var(--stg-danger)" }}>{t(`settings.models.${keyFailure}`)}</span>}
      </label>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <span style={{ fontSize: 12, fontWeight: 500, color: "var(--stg-text-primary)" }}>{t("settings.models.models")}</span>
        <FetchModelsButton probe={{ baseUrl: baseURL.trim(), apiKey: keyValue }}
          existingIds={models.map(m => String(m.id ?? "").trim())}
          onAdopt={(selected) => {
            const byId = new Map(models.map(m => [String(m.id ?? "").trim(), m]));
            for (const s of selected) byId.set(String(s.id ?? "").trim(), byId.get(String(s.id ?? "").trim()) ?? s);
            setModels([...byId.values()]);
          }}
          onError={setFailure}
          disabled={busy} />
      </div>
      <ModelListEditor models={models} onChange={setModels} disabled={busy} />
      {failure !== undefined ? <span style={{ fontSize: 12, color: "var(--stg-danger)" }}>{failure}</span> : null}
      {hint === undefined ? null : <span style={{ fontSize: 11, color: "var(--stg-text-tertiary)" }}>{hint}</span>}
      <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
        <button type="button" className="secondary-btn" disabled={busy} onClick={() => onClose(false)}>{t("settings.models.cancel")}</button>
        <button type="button" className="settings-form button" disabled={busy || !ready}
          style={{ minHeight: 36, padding: "0 14px", border: "none", borderRadius: 18, background: "var(--stg-brand)", color: "#fff", fontWeight: 500, cursor: "pointer" }}
          onClick={() => { void create(); }}>
          {busy ? t("settings.models.creating") : t("settings.models.create")}
        </button>
      </div>
    </div>
  );
}
