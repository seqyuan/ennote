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
 * The "Fetch available models" action and its picker, shared by the provider
 * editor and the custom-provider card. It asks the endpoint the form currently
 * shows (base URL + typed key, unsaved) and, on adopt, hands the selected
 * drafts back to the caller instead of writing anything itself.
 */
export function FetchModelsButton({ probe, existingIds, onAdopt, onError, disabled }: {
  probe: { baseUrl?: string; apiKey?: string };
  existingIds: readonly string[];
  onAdopt: (selected: ModelDraft[]) => void;
  onError?: (message: string) => void;
  disabled?: boolean;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [candidates, setCandidates] = useState<DiscoveredModel[] | null>(null);
  const [fetching, setFetching] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());

  const fetchModels = async () => {
    setFetching(true);
    setCandidates(null);
    try {
      const found = await apiFetch<DiscoveredModel[]>("/v1/provider-profiles/discover-models", {
        method: "POST",
        body: JSON.stringify({ baseUrl: probe.baseUrl || undefined, apiKey: probe.apiKey || undefined }),
      }) ?? [];
      const known = new Set(existingIds);
      setCandidates(found);
      // Candidates already configured start unchecked, so adopting a selection
      // never silently rewrites a row the user already tuned.
      setPicked(new Set(found.filter(candidate => !known.has(candidate.modelName)).map(candidate => candidate.modelName)));
      setOpen(true);
    } catch (reason) {
      onError?.((reason as Error).message);
    } finally {
      setFetching(false);
    }
  };

  const adopt = () => {
    if (!candidates) return;
    onAdopt(candidates.filter(candidate => picked.has(candidate.modelName)).map(toDraft));
    setOpen(false);
    setCandidates(null);
  };

  return (
    <>
      <button type="button" className="secondary-btn" style={{ minHeight: 26, height: 26, padding: "0 10px", borderRadius: 13, fontSize: 11, marginLeft: "auto" }}
        disabled={disabled || fetching} onClick={() => { void fetchModels(); }}>
        {fetching ? t("settings.models.fetching") : t("settings.models.fetchModels")}
      </button>
      {open && candidates && (
        <div className="settings-overlay" style={{ display: "grid", placeItems: "center" }} role="dialog" aria-modal="true" aria-label={t("settings.models.fetchTitle")}>
          <div className="project-create-dialog" style={{ maxWidth: 420 }}>
            <div className="project-create-header">
              <span>{t("settings.models.fetchTitle")}</span>
              <button type="button" className="follow-up-close" aria-label={t("settings.models.close")} title={t("settings.models.close")} onClick={() => { setOpen(false); setCandidates(null); }}>✕</button>
            </div>
            <div className="project-create-form">
              <p style={{ margin: 0, fontSize: 12, lineHeight: "18px", color: "var(--stg-text-secondary)" }}>{t("settings.models.fetchDescription")}</p>
              <div style={{ maxHeight: 240, overflowY: "auto", border: "1px solid var(--stg-border-l2)", borderRadius: 6 }}>
                {candidates.map(candidate => (
                  <label key={candidate.modelName} style={{ display: "flex", alignItems: "center", gap: 8, padding: "6px 10px", borderBottom: "1px solid var(--stg-border-l2)", fontSize: 12, cursor: "pointer" }}>
                    <input type="checkbox" checked={picked.has(candidate.modelName)} onChange={() => {
                      setPicked(current => { const next = new Set(current); if (!next.delete(candidate.modelName)) next.add(candidate.modelName); return next; });
                    }} />
                    <code style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{candidate.modelName}</code>
                  </label>
                ))}
              </div>
              <div className="project-create-actions" style={{ justifyContent: "flex-end" }}>
                <button type="button" className="secondary-btn" onClick={() => { setOpen(false); setCandidates(null); }}>{t("settings.models.cancel")}</button>
                <button type="button" className="project-create-submit" onClick={adopt}>{t("settings.models.fetchAdopt")}</button>
              </div>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
