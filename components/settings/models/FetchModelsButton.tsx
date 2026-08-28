"use client";

import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { apiFetch } from "@/lib/worker-api.client";
import type { ModelDraft } from "@/lib/model-draft";
import type { DiscoveredModel } from "@/components/settings/types";

/** Convert a discovered catalog entry into an editable model draft. */
function toDraft(candidate: DiscoveredModel): ModelDraft {
  return {
    id: candidate.modelName,
    ...(candidate.displayName ? { name: candidate.displayName } : {}),
    ...(candidate.contextWindow ? { contextWindow: candidate.contextWindow } : {}),
    ...(candidate.maxOutputTokens ? { maxTokens: candidate.maxOutputTokens } : {}),
  };
}

/**
 * The "Fetch available models" action and its picker, shared by the model-list
 * editor. It asks the endpoint the form currently shows (base URL + typed key,
 * unsaved) and, on adopt, hands the selected drafts back to the caller.
 */
export function FetchModelsButton({ probe, existingIds, onAdopt, onError, disabled, blockedReason }: {
  probe: { baseUrl?: string; apiKey?: string };
  existingIds: readonly string[];
  onAdopt: (selected: ModelDraft[]) => void;
  onError?: (message: string) => void;
  disabled?: boolean;
  blockedReason?: string;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [candidates, setCandidates] = useState<DiscoveredModel[] | null>(null);
  const [fetching, setFetching] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());

  const askable = Boolean(probe.baseUrl && probe.baseUrl.length > 0);
  const title = blockedReason ?? (askable ? undefined : t("settings.models.fetchNeedsBaseUrl"));

  const fetchModels = async () => {
    setFetching(true);
    setCandidates(null);
    try {
      const found = await apiFetch<DiscoveredModel[]>("/v1/provider-profiles/discover-models", {
        method: "POST",
        body: JSON.stringify({ baseUrl: probe.baseUrl || undefined, apiKey: probe.apiKey || undefined }),
      }) ?? [];
      if (found.length === 0) {
        onError?.(t("settings.models.fetchEmpty"));
        return;
      }
      const known = new Set(existingIds);
      setCandidates(found);
      setPicked(new Set(found.filter(candidate => !known.has(candidate.modelName)).map(candidate => candidate.modelName)));
      setOpen(true);
    } catch (reason) {
      onError?.((reason as Error).message);
    } finally {
      setFetching(false);
    }
  };

  const close = () => {
    setOpen(false);
    setCandidates(null);
  };

  const adopt = () => {
    if (!candidates) return;
    onAdopt(candidates.filter(candidate => picked.has(candidate.modelName)).map(toDraft));
    close();
  };

  const allPicked = candidates !== null && candidates.length > 0
    && candidates.every(candidate => picked.has(candidate.modelName));

  const toggleAll = () => {
    if (!candidates) return;
    setPicked(allPicked ? new Set() : new Set(candidates.map(candidate => candidate.modelName)));
  };

  return (
    <>
      <button
        type="button"
        className="settings-models-link"
        disabled={disabled || fetching || !askable || blockedReason !== undefined}
        title={title}
        onClick={() => { void fetchModels(); }}
      >
        {fetching ? t("settings.models.fetching") : t("settings.models.fetchModels")}
      </button>
      {open && candidates && (
        <div className="settings-overlay" style={{ display: "grid", placeItems: "center" }} role="dialog" aria-modal="true" aria-label={t("settings.models.fetchTitle")}>
          <div className="project-create-dialog settings-models-fetch">
            <div className="project-create-header">
              <span>{t("settings.models.fetchTitle")}</span>
              <button type="button" className="follow-up-close" aria-label={t("settings.models.close")} title={t("settings.models.close")} onClick={close}>✕</button>
            </div>
            <div className="project-create-form">
              <p className="settings-models-hint">{t("settings.models.fetchDescription")}</p>
              <div className="settings-models-fetch-actions">
                <button type="button" className="settings-models-link" onClick={toggleAll}>
                  {t(allPicked ? "settings.models.fetchDeselectAll" : "settings.models.fetchSelectAll")}
                </button>
              </div>
              <ul className="settings-models-candidates">
                {candidates.map(candidate => (
                  <li key={candidate.modelName} className="settings-models-candidate">
                    <label className="settings-models-candidate-label">
                      <input type="checkbox" checked={picked.has(candidate.modelName)} onChange={() => {
                        setPicked(current => {
                          const next = new Set(current);
                          if (!next.delete(candidate.modelName)) next.add(candidate.modelName);
                          return next;
                        });
                      }} />
                      <span className="settings-models-candidate-id">{candidate.modelName}</span>
                    </label>
                  </li>
                ))}
              </ul>
              <div className="project-create-actions" style={{ justifyContent: "flex-end" }}>
                <button type="button" className="secondary-btn" onClick={close}>{t("settings.models.cancel")}</button>
                <button type="button" className="secondary-btn" onClick={adopt}>{t("settings.models.fetchAdopt")}</button>
              </div>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
