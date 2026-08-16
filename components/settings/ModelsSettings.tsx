"use client";

import { Trash2 } from "lucide-react";
import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { CredentialDot } from "@/components/settings/models/CredentialDot";
import { CustomProviderCard } from "@/components/settings/models/CustomProviderCard";
import { DeleteProviderModal } from "@/components/settings/models/DeleteProviderModal";
import { ProviderEditor } from "@/components/settings/models/ProviderEditor";
import { apiFetch } from "@/lib/worker-api.client";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";

/** Models settings: one row per configured provider (name, custom tag, API-key
 *  dot, Edit/Delete) and, when editing, the provider editor card (single key
 *  field + custom-settings fold + model list). Matches the dsh Models section:
 *  model management and endpoint interrogation live inside the editor, not on
 *  the collapsed row. */
export function ModelsSettings({ providers, models, refresh, setError }: {
  providers: ProviderProfile[];
  models: ModelProfile[];
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const t = useT();
  const [customOpen, setCustomOpen] = useState(false);
  const [savedName, setSavedName] = useState<string | null>(null);

  return <section className="settings-tab-section" aria-labelledby="settings-models-heading">
    <header><h2 id="settings-models-heading">{t("settings.models.title")}</h2>
      <p>{t("settings.models.intro")}</p></header>
    {savedName && (
      <p role="status" aria-live="polite" style={{ margin: 0, fontSize: 12, lineHeight: "18px", color: "var(--stg-text-tertiary)" }}>
        {t("settings.models.savedProvider").replace("{provider}", savedName)}
      </p>
    )}
    {!customOpen ? (
      <button type="button" className="secondary-btn" style={{ marginBottom: 12 }} onClick={() => setCustomOpen(true)}>
        {t("settings.models.customAdd")}
      </button>
    ) : (
      <CustomProviderCard taken={providers.map(provider => provider.id)}
        onClose={() => setCustomOpen(false)} refresh={refresh} setError={setError} onSaved={setSavedName} />
    )}
    <div className="settings-list">
      {providers.map(provider => (
        <ProviderCard key={provider.id} provider={provider}
          models={models.filter(model => model.providerId === provider.id)}
          refresh={refresh} setError={setError} onSaved={setSavedName} />
      ))}
      {providers.length === 0 && (
        <div className="settings-empty">No providers yet — add one above to start wiring models.</div>
      )}
    </div>
  </section>;
}

function ProviderCard({ provider, models, refresh, setError, onSaved }: {
  provider: ProviderProfile;
  models: ModelProfile[];
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
  onSaved: (name: string) => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteFailure, setDeleteFailure] = useState<string | undefined>(undefined);
  const [editing, setEditing] = useState(false);

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
      <div className="settings-primary" style={{ minWidth: 0 }}>
        <strong style={{ display: "flex", alignItems: "center", gap: 5 }}>
          {provider.name}
          {provider.custom ? <span style={{ fontSize: 10, fontWeight: 500, color: "var(--stg-text-tertiary)", border: "1px solid var(--stg-border-l2)", borderRadius: 4, padding: "0 5px", lineHeight: "16px" }}>{t("settings.models.customTag")}</span> : null}
          <CredentialDot configured={provider.credentialConfigured} missing={!provider.credentialConfigured} />
        </strong>
      </div>
      <div className="provider-controls">
        <button type="button" className="secondary-btn" disabled={busy} onClick={() => setEditing(open => !open)}>
          {t("settings.models.edit")}
        </button>
        <button type="button" className="secondary-btn" title={t("settings.models.removeProvider").replace("{provider}", provider.name)} aria-label={t("settings.models.removeProvider").replace("{provider}", provider.name)}
          disabled={busy} onClick={() => setDeleteOpen(true)}>
          <Trash2 size={13} aria-hidden="true" />
        </button>
      </div>
    </div>
    {editing && (
      <ProviderEditor provider={provider} models={models}
        onClose={(changed) => { setEditing(false); if (changed) onSaved(provider.name); }}
        refresh={refresh} setError={setError} />
    )}
    <DeleteProviderModal open={deleteOpen} providerName={provider.name} busy={busy} failure={deleteFailure}
      onCancel={() => { if (!busy) { setDeleteOpen(false); setDeleteFailure(undefined); } }} onConfirm={deleteProvider} />
  </div>;
}
