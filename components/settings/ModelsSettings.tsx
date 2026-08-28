"use client";

import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { CredentialDot } from "@/components/settings/models/CredentialDot";
import { CustomProviderCard } from "@/components/settings/models/CustomProviderCard";
import { DeleteProviderModal } from "@/components/settings/models/DeleteProviderModal";
import { ProviderEditor } from "@/components/settings/models/ProviderEditor";
import { apiFetch } from "@/lib/worker-api.client";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";

function IconPlus({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden>
      <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

/** One provider row joined from the profile list and the built-in directory:
 *  directory rows carry `directory` (dormant — no stored profile yet) and are
 *  what the add flow adopts; stored profiles always override their directory
 *  entry. Mirrors dsh's Models section: rows expose only confirmed API-key
 *  state through accessible dots, model management and endpoint interrogation
 *  live inside the editor, and the two ways to gain a provider (adopt one the
 *  directory ships, or declare one it does not) are equal siblings. */
export function ModelsSettings({ providers, models, refresh, setError }: {
  providers: ProviderProfile[];
  models: ModelProfile[];
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
}) {
  const t = useT();
  const [editing, setEditing] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [declaring, setDeclaring] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ProviderProfile | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteFailure, setDeleteFailure] = useState<string | undefined>(undefined);
  const [savedName, setSavedName] = useState<string | null>(null);
  // Setup cards the user closed stay closed for the session; the provider
  // falls back to an ordinary row and reopens through Edit.
  const [dismissedSetup, setDismissedSetup] = useState<ReadonlySet<string>>(() => new Set());

  // One fact decides both first-run postures: whether the user already has a
  // provider to talk to (dsh `anyUsable`).
  const anyUsable = providers.some(provider => provider.credentialConfigured);
  const dormant = providers.filter(provider => provider.directory);
  const addProvider = adding
    ? providers.find(provider => provider.directory && provider.id === editing) ?? null
    : null;

  const announceSaved = (name: string): void => { setSavedName(name); };

  const closeDelete = (): void => {
    if (deleting) return;
    setDeleteTarget(null);
    setDeleteFailure(undefined);
  };

  const confirmDelete = async (): Promise<void> => {
    if (deleteTarget === null || deleting) return;
    setDeleting(true);
    setDeleteFailure(undefined);
    try {
      await apiFetch(`/v1/provider-profiles/${encodeURIComponent(deleteTarget.id)}`, { method: "DELETE" });
      setError(null);
      setDeleteTarget(null);
      await refresh();
    } catch (reason) {
      setDeleteFailure((reason as Error).message);
    } finally {
      setDeleting(false);
    }
  };

  return <section className="settings-tab-section" aria-labelledby="settings-models-heading">
    <header><h2 id="settings-models-heading">{t("settings.models.title")}</h2>
      <p>{t("settings.models.intro")}</p></header>
    {savedName !== null && (
      <p className="settings-models-saved" role="status" aria-live="polite">
        {t("settings.models.savedProvider").replace("{provider}", savedName)}
      </p>
    )}
    <ul className="settings-models-rows">
      {providers.map(provider => {
        if (!anyUsable && provider.directory && !dismissedSetup.has(provider.id)) {
          // First-run posture: the provider exists in the directory but has
          // no key — the setup card IS its presence on the page, until the
          // user closes it.
          return (
            <li key={provider.id} className="settings-models-setupcard">
              <ProviderEditor
                provider={provider}
                models={[]}
                creating
                refresh={refresh}
                setError={setError}
                onSaved={announceSaved}
                onClose={() => {
                  setDismissedSetup(previous => new Set([...previous, provider.id]));
                }}
              />
            </li>
          );
        }
        const open = !adding && editing === provider.id;
        return (
          <li key={provider.id} className="settings-models-rowcard">
            <div className="settings-models-rowhead">
              <span className="settings-models-rowidentity">
                <span className="settings-models-rowname">{provider.name}</span>
                {provider.custom
                  ? <span className="settings-models-rowtag">{t("settings.models.customTag")}</span>
                  : null}
                <CredentialDot configured={provider.credentialConfigured} missing={!provider.credentialConfigured} />
              </span>
              <span className="settings-models-rowactions">
                <button
                  type="button"
                  className="settings-models-btn"
                  aria-label={t("settings.models.editProvider").replace("{provider}", provider.name)}
                  onClick={() => {
                    setSavedName(null);
                    setDeclaring(false);
                    setAdding(false);
                    setEditing(open ? null : provider.id);
                  }}
                >
                  {t("settings.models.edit")}
                </button>
                {provider.directory ? null : (
                  <button
                    type="button"
                    className="settings-models-btn settings-models-danger"
                    aria-label={t("settings.models.removeProvider").replace("{provider}", provider.name)}
                    onClick={() => {
                      setSavedName(null);
                      setDeleteFailure(undefined);
                      setDeleteTarget(provider);
                    }}
                  >
                    {t("settings.models.remove")}
                  </button>
                )}
              </span>
            </div>
            {open && (
              // A dismissed directory row reopens through Edit in create
              // mode: no stored profile exists yet, so the card must POST.
              <ProviderEditor
                provider={provider}
                models={models.filter(model => model.providerId === provider.id)}
                creating={provider.directory}
                refresh={refresh}
                setError={setError}
                onSaved={announceSaved}
                onClose={() => { setEditing(null); }}
              />
            )}
          </li>
        );
      })}
    </ul>
    <div className="settings-models-addblock">
      {addProvider !== null && (
        <div className="settings-models-addcard">
          <div className="settings-models-field">
            <span className="settings-models-fieldlabel">{t("settings.models.provider")}</span>
            <select
              className="settings-models-input settings-models-select"
              value={addProvider.id}
              aria-label={t("settings.models.provider")}
              onChange={(event) => { setEditing(event.target.value); }}
            >
              {dormant.map(row => (
                <option key={row.id} value={row.id}>{row.name}</option>
              ))}
            </select>
          </div>
          <ProviderEditor
            key={addProvider.id}
            provider={addProvider}
            models={[]}
            creating
            hideTitle
            refresh={refresh}
            setError={setError}
            onSaved={announceSaved}
            onClose={() => { setAdding(false); setEditing(null); }}
          />
        </div>
      )}
      {declaring && (
        <div className="settings-models-addcard">
          <CustomProviderCard
            taken={providers.map(provider => provider.id)}
            refresh={refresh}
            setError={setError}
            onSaved={announceSaved}
            onClose={(changed) => { setDeclaring(false); if (changed) void refresh(); }}
          />
        </div>
      )}
      {addProvider === null && !declaring && (
        <div className="settings-models-addactions">
          <button
            type="button"
            className="settings-models-addbutton"
            disabled={dormant.length === 0}
            onClick={() => {
              setSavedName(null);
              setDeclaring(false);
              setAdding(true);
              setEditing(dormant[0]?.id ?? null);
            }}
          >
            <IconPlus />
            {t("settings.models.add")}
          </button>
          <button
            type="button"
            className="settings-models-addbutton"
            onClick={() => {
              setSavedName(null);
              setAdding(false);
              setEditing(null);
              setDeclaring(true);
            }}
          >
            <IconPlus />
            {t("settings.models.customAdd")}
          </button>
        </div>
      )}
    </div>
    <DeleteProviderModal
      open={deleteTarget !== null}
      providerName={deleteTarget?.name ?? ""}
      busy={deleting}
      failure={deleteFailure}
      onCancel={closeDelete}
      onConfirm={() => { void confirmDelete(); }}
    />
  </section>;
}
