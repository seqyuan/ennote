"use client";

import { ChevronRight, Download, Trash2, WandSparkles } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { SecretTextInput } from "@/components/settings/SecretTextInput";
import { CredentialDot } from "@/components/settings/models/CredentialDot";
import { CustomProviderCard } from "@/components/settings/models/CustomProviderCard";
import { DeleteProviderModal } from "@/components/settings/models/DeleteProviderModal";
import { ProviderEditor } from "@/components/settings/models/ProviderEditor";
import type { DiscoveredModel, ModelProfile, ProviderDiagnostic, ProviderProfile } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

/** Providers own their model catalog; credential values are write-only. */
export function ModelsSettings({ providers, models, refresh, setError }: {
  providers: ProviderProfile[];
  models: ModelProfile[];
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [customOpen, setCustomOpen] = useState(false);

  return <section className="settings-tab-section" aria-labelledby="settings-models-heading">
    <header><h2 id="settings-models-heading">Models</h2>
      <p>Providers own their model catalog. Add a connection, then import models from its API or add them by hand.</p></header>
    <AddProviderForm refresh={refresh} setError={setError} />
    {!customOpen ? (
      <button type="button" className="secondary-btn" style={{ marginBottom: 12 }} onClick={() => setCustomOpen(true)}>
        Add a custom provider
      </button>
    ) : (
      <CustomProviderCard taken={providers.map(provider => provider.id)}
        onClose={() => setCustomOpen(false)} refresh={refresh} setError={setError} />
    )}
    <div className="settings-list">
      {providers.map(provider => (
        <ProviderCard key={provider.id} provider={provider}
          models={models.filter(model => model.providerId === provider.id)}
          collapsed={Boolean(collapsed[provider.id])}
          onToggleCollapsed={() => setCollapsed(cur => ({ ...cur, [provider.id]: !cur[provider.id] }))}
          refresh={refresh} setError={setError} />
      ))}
      {providers.length === 0 && (
        <div className="settings-empty">No providers yet — add one above to start wiring models.</div>
      )}
    </div>
  </section>;
}

function AddProviderForm({ refresh, setError }: {
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const [apiKey, setApiKey] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    try {
      await apiFetch("/v1/provider-profiles", { method: "POST", body: JSON.stringify({
        name: data.get("name"), providerType: "openai-compatible",
        baseUrl: data.get("baseUrl"), apiKey,
      }) });
      form.reset();
      setApiKey("");
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }
  return <form className="settings-form provider-form" onSubmit={submit}>
    <label>Name<input name="name" required placeholder="openai-main" /></label>
    <label>Base URL<input name="baseUrl" type="url" required placeholder="https://api.openai.com/v1" /></label>
    <label>API key<SecretTextInput value={apiKey} onChange={setApiKey} /></label>
    <button type="submit">Add provider</button>
  </form>;
}

function ProviderCard({ provider, models, collapsed, onToggleCollapsed, refresh, setError }: {
  provider: ProviderProfile;
  models: ModelProfile[];
  collapsed: boolean;
  onToggleCollapsed: () => void;
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const [diagnostic, setDiagnostic] = useState<ProviderDiagnostic | null>(null);
  const [checking, setChecking] = useState(false);
  const [discoverOpen, setDiscoverOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteFailure, setDeleteFailure] = useState<string | undefined>(undefined);
  const [editing, setEditing] = useState(false);
  const requestVersion = useRef(0);
  const controller = useRef<AbortController | null>(null);

  useEffect(() => () => controller.current?.abort(), []);

  const testProvider = useCallback(async () => {
    controller.current?.abort();
    const activeController = new AbortController();
    controller.current = activeController;
    const version = ++requestVersion.current;
    setChecking(true);
    setDiagnostic(null);
    try {
      const result = await apiFetch<ProviderDiagnostic>(`/v1/provider-profiles/${encodeURIComponent(provider.id)}/test`, {
        method: "POST",
        body: JSON.stringify(models[0] ? { modelProfileId: models[0].id } : {}),
        signal: activeController.signal,
      });
      if (!activeController.signal.aborted && requestVersion.current === version) setDiagnostic(result);
    } catch (reason) {
      if (!activeController.signal.aborted && requestVersion.current === version) {
        setError((reason as Error).message);
      }
    } finally {
      if (!activeController.signal.aborted && requestVersion.current === version) setChecking(false);
    }
  }, [models, provider.id, setError]);

  const deleteProvider = async () => {
    setBusy(true);
    setDeleteFailure(undefined);
    try {
      await apiFetch(`/v1/provider-profiles/${encodeURIComponent(provider.id)}`, { method: "DELETE" });
      setError(null);
      setDeleteOpen(false);
      await refresh();
    } catch (reason) {
      setDeleteFailure((reason as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return <div className="settings-row provider-settings-row">
    <div className="provider-head">
    <div className="settings-primary" style={{ cursor: "pointer", minWidth: 0 }} onClick={onToggleCollapsed}>
      <strong style={{ display: "flex", alignItems: "center", gap: 5 }}>
        <ChevronRight size={13} style={{ transform: collapsed ? "none" : "rotate(90deg)", transition: "transform 0.15s", color: "var(--text-dim)" }} />
        {provider.name}
        {provider.custom ? <span style={{ fontSize: 10, fontWeight: 500, color: "var(--stg-text-tertiary)", border: "1px solid var(--stg-border-l2)", borderRadius: 4, padding: "0 5px", lineHeight: "16px" }}>Custom</span> : null}
        <CredentialDot configured={provider.credentialConfigured} missing={!provider.credentialConfigured} />
      </strong>
      <span>{provider.baseUrl} · {models.length} {models.length === 1 ? "model" : "models"} · {provider.credentialConfigured ? "Credential saved" : "No credential"}</span>
    </div>
    <div className="provider-controls">
      <button type="button" className="secondary-btn" disabled={busy} onClick={() => setEditing(open => !open)}>
        {editing ? "Cancel edit" : "Edit"}
      </button>
      <button type="button" className="secondary-btn" disabled={busy} onClick={testProvider}>
        {checking ? "Retest" : "Test"}
      </button>
      <button type="button" className="secondary-btn" onClick={() => setDiscoverOpen(true)} disabled={busy}>
        <WandSparkles size={13} aria-hidden="true" /> Discover models
      </button>
      <button type="button" className="secondary-btn" title="Delete provider" aria-label={`Delete provider ${provider.name}`}
        disabled={busy} onClick={() => setDeleteOpen(true)}>
        <Trash2 size={13} aria-hidden="true" />
      </button>
    </div>
    </div>
    {diagnostic && (
      <div className="provider-diagnostic" data-testid={`provider-diagnostic-${provider.id}`}>
        <div className={`diagnostic-summary diagnostic-${diagnostic.status}`}>
          <strong>{diagnostic.status === "ready" ? "Ready" : "Failed"}</strong>
          <span>{diagnostic.modelName ?? "No model"} · {diagnostic.latencyMs} ms</span>
        </div>
        {diagnostic.failure && <span className="diagnostic-failure">{diagnostic.failure.message}</span>}
      </div>
    )}
    {editing && (
      <ProviderEditor provider={provider} models={models} onClose={() => setEditing(false)} refresh={refresh} setError={setError} />
    )}
    {!collapsed && (
      <div style={{ display: "flex", flexDirection: "column", gap: 8, paddingTop: 8 }}>
        {models.map(model => (
          <ModelRow key={model.id} provider={provider} model={model} refresh={refresh} setError={setError} />
        ))}
        {models.length === 0 && <div className="settings-empty">No models yet — Discover or add one below.</div>}
        <AddModelForm provider={provider} refresh={refresh} setError={setError} />
      </div>
    )}
    {discoverOpen && (
      <DiscoverModelsDialog provider={provider} existingModels={models} onClose={() => setDiscoverOpen(false)} refresh={refresh} setError={setError} />
    )}
    <DeleteProviderModal open={deleteOpen} providerName={provider.name} busy={busy} failure={deleteFailure}
      onCancel={() => { if (!busy) { setDeleteOpen(false); setDeleteFailure(undefined); } }} onConfirm={deleteProvider} />
  </div>;
}

function ModelRow({ provider, model, refresh, setError }: {
  provider: ProviderProfile;
  model: ModelProfile;
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<ProviderDiagnostic | null>(null);
  const [busy, setBusy] = useState(false);

  const testModel = async () => {
    setTesting(true);
    setResult(null);
    try {
      const r = await apiFetch<ProviderDiagnostic>(`/v1/provider-profiles/${encodeURIComponent(provider.id)}/test`, {
        method: "POST", body: JSON.stringify({ modelProfileId: model.id }),
      });
      setResult(r);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setTesting(false);
    }
  };

  const makeDefault = async () => {
    try {
      await apiFetch(`/v1/model-profiles/${encodeURIComponent(model.id)}/default`, { method: "PUT" });
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  };

  const deleteModel = async () => {
    if (!window.confirm(`Delete model "${model.displayName || model.modelName}"?`)) return;
    setBusy(true);
    try {
      await apiFetch(`/v1/model-profiles/${encodeURIComponent(model.id)}`, { method: "DELETE" });
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const capabilities = [
    model.supportsToolUse ? "Tool" : null,
    model.supportsVision ? "Vision" : null,
    model.supportsThinking ? "Thinking" : null,
  ].filter(Boolean).join("/");

  return <div className="settings-row">
    <div className="settings-primary">
      <strong>{model.displayName || model.modelName}</strong>
      <span>{model.modelName} · {model.contextWindow.toLocaleString()} ctx · {model.inputCostUsdMicrosPerMillion.toLocaleString()}/{model.outputCostUsdMicrosPerMillion.toLocaleString()} uUSD/M · {capabilities || "no caps"}</span>
      {result && (
        <span className={`diagnostic-summary diagnostic-${result.status}`} style={{ marginTop: 4 }}>
          {result.status === "ready" ? "OK" : "Failed"} · {result.latencyMs} ms{result.failure ? ` · ${result.failure.message}` : ""}
        </span>
      )}
    </div>
    <div className="provider-controls">
      <button type="button" className="secondary-btn" disabled={testing || busy} onClick={testModel}>{testing ? "…" : "Test"}</button>
      <button type="button" className="secondary-btn" disabled={model.isDefault || busy} onClick={makeDefault}>
        {model.isDefault ? "Default" : "Make default"}
      </button>
      <button type="button" className="secondary-btn" title="Delete model" aria-label={`Delete model ${model.displayName || model.modelName}`}
        disabled={busy} onClick={deleteModel}>
        <Trash2 size={13} aria-hidden="true" />
      </button>
    </div>
  </div>;
}

function AddModelForm({ provider, refresh, setError }: {
  provider: ProviderProfile;
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const [open, setOpen] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    try {
      await apiFetch("/v1/model-profiles", { method: "POST", body: JSON.stringify({
        providerId: provider.id, modelName: data.get("modelName"), displayName: data.get("displayName"),
        contextWindow: Number(data.get("contextWindow")) || 131072,
        maxOutputTokens: Number(data.get("maxOutputTokens")) || 16384,
        inputCostUsdMicrosPerMillion: Number(data.get("inputCostUsdMicrosPerMillion")) || 0,
        outputCostUsdMicrosPerMillion: Number(data.get("outputCostUsdMicrosPerMillion")) || 0,
        supportsVision: data.get("supportsVision") === "on", supportsToolUse: true, supportsThinking: data.get("supportsThinking") === "on",
      }) });
      form.reset();
      setOpen(false);
      setError(null);
      await refresh();
    } catch (reason) {
      setError((reason as Error).message);
    }
  }
  return <div>
    {!open && <button type="button" className="secondary-btn" onClick={() => setOpen(true)}>+ Add model</button>}
    {open && (
      <form className="settings-form model-form" onSubmit={submit}>
        <label>API model ID<input name="modelName" required placeholder="gpt-4o" /></label>
        <label>Display name<input name="displayName" /></label>
        <label>Context window<input name="contextWindow" type="number" min="1" defaultValue="131072" /></label>
        <label>Max output<input name="maxOutputTokens" type="number" min="1" defaultValue="16384" /></label>
        <label>In USD/1M<input name="inputCostUsdMicrosPerMillion" type="number" min="0" defaultValue="0" /></label>
        <label>Out USD/1M<input name="outputCostUsdMicrosPerMillion" type="number" min="0" defaultValue="0" /></label>
        <div className="settings-toggles">
          <label><input name="supportsVision" type="checkbox" /> Vision</label>
          <label><input name="supportsThinking" type="checkbox" /> Thinking</label>
        </div>
        <div style={{ display: "flex", gap: 6 }}>
          <button type="submit">Add model</button>
          <button type="button" className="secondary-btn" onClick={() => setOpen(false)}>Cancel</button>
        </div>
      </form>
    )}
  </div>;
}

function DiscoverModelsDialog({ provider, existingModels, onClose, refresh, setError }: {
  provider: ProviderProfile;
  existingModels: ModelProfile[];
  onClose: () => void;
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const [catalog, setCatalog] = useState<DiscoveredModel[] | null>(null);
  const [fetching, setFetching] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [importing, setImporting] = useState(false);

  const existingNames = new Set(existingModels.map(model => model.modelName));

  const fetchCatalog = async () => {
    setFetching(true);
    setCatalog(null);
    try {
      const result = await apiFetch<DiscoveredModel[]>("/v1/provider-profiles/discover-models", {
        method: "POST",
        body: JSON.stringify({ providerId: provider.id }),
      });
      setCatalog(result ?? []);
      setSelected(new Set((result ?? []).map(model => model.modelName)));
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setFetching(false);
    }
  };

  const importSelected = async () => {
    if (!catalog) return;
    setImporting(true);
    try {
      for (const model of catalog) {
        if (!selected.has(model.modelName) || existingNames.has(model.modelName)) continue;
        await apiFetch("/v1/model-profiles", { method: "POST", body: JSON.stringify({
          providerId: provider.id, modelName: model.modelName, displayName: model.displayName ?? "",
          contextWindow: model.contextWindow || 131072, maxOutputTokens: model.maxOutputTokens || 16384,
          inputCostUsdMicrosPerMillion: 0, outputCostUsdMicrosPerMillion: 0,
          supportsVision: model.supportsVision, supportsToolUse: true, supportsThinking: model.supportsThinking,
        }) });
      }
      setError(null);
      await refresh();
      onClose();
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setImporting(false);
    }
  };

  const toggleAll = () => {
    if (!catalog) return;
    if (selected.size === catalog.length) setSelected(new Set());
    else setSelected(new Set(catalog.map(model => model.modelName)));
  };

  return <div className="settings-overlay" style={{ display: "grid", placeItems: "center" }}>
    <div className="project-create-dialog" role="dialog" aria-modal="true" aria-label="Discover models">
      <div className="project-create-header">
        <span><Download size={15} aria-hidden="true" /> Discover models · {provider.name}</span>
        <button type="button" className="follow-up-close" aria-label="Close" title="Close" onClick={onClose}>✕</button>
      </div>
      <div className="project-create-form">
        <div className="project-create-actions">
          <button type="button" className="secondary-btn" onClick={fetchCatalog} disabled={fetching}>
            {fetching ? "Fetching…" : "Fetch catalog"}
          </button>
        </div>
        {catalog && (
          <>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginTop: 8 }}>
              <span style={{ fontSize: 11, color: "var(--text-muted)" }}>
                {catalog.length} models · {selected.size} selected · {existingNames.size} already imported
              </span>
              <button type="button" className="secondary-btn" onClick={toggleAll} style={{ minHeight: 26, padding: "2px 10px" }}>
                {selected.size === catalog.length ? "Clear all" : "Select all"}
              </button>
            </div>
            <div style={{ maxHeight: 260, overflowY: "auto", border: "1px solid var(--border)", borderRadius: 6, marginTop: 6 }}>
              {catalog.map(model => {
                const already = existingNames.has(model.modelName);
                return <label key={model.modelName} style={{ display: "flex", alignItems: "center", gap: 8, padding: "7px 10px", borderBottom: "1px solid var(--border)", fontSize: 12, cursor: already ? "default" : "pointer" }}>
                  <input type="checkbox" checked={selected.has(model.modelName)} disabled={already}
                    onChange={() => setSelected(cur => {
                      const next = new Set(cur);
                      if (next.has(model.modelName)) next.delete(model.modelName); else next.add(model.modelName);
                      return next;
                    })} />
                  <code style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{model.modelName}</code>
                  {already && <span style={{ fontSize: 10, color: "var(--text-dim)" }}>imported</span>}
                </label>;
              })}
            </div>
            <div className="project-create-actions">
              <button type="button" className="project-create-submit" onClick={importSelected} disabled={importing || selected.size === 0}>
                {importing ? "Importing…" : `Import ${selected.size} selected`}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  </div>;
}
