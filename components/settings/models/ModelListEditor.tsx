"use client";

import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { FetchModelsButton } from "@/components/settings/models/FetchModelsButton";
import { formatCapacity, parseCapacity } from "@/lib/capacity";
import type { ModelDraft } from "@/lib/model-draft";

/** The two token counts edited as K/M-suffixed text behind a row's disclosure. */
type CapacityField = "contextWindow" | "maxTokens";

function textOf(model: ModelDraft, key: string): string {
  const value = model[key];
  return typeof value === "string" ? value : "";
}

function numberOf(model: ModelDraft, key: string): number | undefined {
  const value = model[key];
  return typeof value === "number" ? value : undefined;
}

function IconChevron({ open }: { open: boolean }) {
  return (
    <svg
      width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden
      style={{ transform: open ? "rotate(90deg)" : undefined, transition: "transform 120ms ease" }}
    >
      <path d="M6 3.5L10.5 8L6 12.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function IconTrash() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden>
      <path
        d="M2.5 4h11M6.5 4V2.5h3V4M4 4l.7 9a1 1 0 001 .9h4.6a1 1 0 001-.9L12 4M6.5 6.8v4.4M9.5 6.8v4.4"
        stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round"
      />
    </svg>
  );
}

function IconPlus() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden>
      <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

/**
 * The model list of one provider profile: one bordered entry per model (id +
 * display name) with capacities behind a per-row disclosure, plus add/delete
 * and the "Fetch available models" interrogation. Matches dsh ModelListEditor.
 */
export function ModelListEditor({ models, onChange, overridden, onReset, disabled, probe, probeBlocked, onFetchError }: {
  models: readonly ModelDraft[];
  onChange: (models: ModelDraft[]) => void;
  overridden?: boolean;
  onReset?: () => void;
  disabled?: boolean;
  probe?: { baseUrl?: string; apiKey?: string };
  probeBlocked?: string;
  onFetchError?: (message: string) => void;
}) {
  const t = useT();
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(() => new Set());
  const [editing, setEditing] = useState<ReadonlyMap<string, string>>(() => new Map());

  const bufferKey = (index: number, field: CapacityField): string => `${String(index)}:${field}`;

  const patch = (index: number, next: Record<string, string | number | undefined>): void => {
    onChange(models.map((model, at) => {
      if (at !== index) return model;
      const cleared = new Set(
        Object.entries(next).filter(([, value]) => value === undefined || value === "").map(([key]) => key),
      );
      return Object.fromEntries(
        Object.entries({ ...model, ...next }).filter(([key]) => !cleared.has(key)),
      );
    }));
  };

  const editCapacity = (index: number, field: CapacityField, text: string): void => {
    setEditing(current => new Map(current).set(bufferKey(index, field), text));
    patch(index, { [field]: parseCapacity(text) });
  };

  const capacityText = (model: ModelDraft, index: number, field: CapacityField): string => {
    const typed = editing.get(bufferKey(index, field));
    if (typed !== undefined) return typed;
    const value = numberOf(model, field);
    return value === undefined ? "" : formatCapacity(value);
  };

  const toggleExpanded = (index: number): void => {
    setExpanded((current) => {
      const next = new Set(current);
      if (!next.delete(index)) next.add(index);
      return next;
    });
  };

  const reindexOnRemove = (current: ReadonlyMap<string, string>, index: number): Map<string, string> => {
    const next = new Map<string, string>();
    for (const [key, value] of current) {
      const at = Number(key.slice(0, key.indexOf(":")));
      if (at === index) continue;
      next.set(at > index ? key.replace(/^\d+/, String(at - 1)) : key, value);
    }
    return next;
  };

  const remove = (index: number): void => {
    onChange(models.filter((_model, at) => at !== index));
    setExpanded((current) => {
      const next = new Set<number>();
      for (const at of current) {
        if (at < index) next.add(at);
        else if (at > index) next.add(at - 1);
      }
      return next;
    });
    setEditing(current => reindexOnRemove(current, index));
  };

  const draftId = (draft: ModelDraft): string => (typeof draft.id === "string" ? draft.id : "").trim();

  return (
    <section className="settings-models-catalog" aria-label={t("settings.models.models")}>
      <div className="settings-models-list-head">
        <div className="settings-models-catalog-heading">
          <span className="settings-models-catalog-title">{t("settings.models.models")}</span>
          {overridden !== undefined && (
            <span className="settings-models-catalog-meta">
              {overridden ? t("settings.models.modelsCustomized") : t("settings.models.modelsInherited")}
            </span>
          )}
        </div>
        {overridden === true && onReset !== undefined && (
          <button type="button" className="settings-models-link" disabled={disabled} onClick={onReset}>
            {t("settings.models.resetModels")}
          </button>
        )}
        {probe !== undefined && (
          <FetchModelsButton
            probe={probe}
            existingIds={models.map(draftId)}
            onAdopt={(selected) => {
              const byId = new Map(models.map(d => [draftId(d), d]));
              for (const s of selected) byId.set(draftId(s), byId.get(draftId(s)) ?? s);
              onChange([...byId.values()]);
            }}
            onError={onFetchError}
            disabled={disabled}
            blockedReason={probeBlocked}
          />
        )}
      </div>
      {models.length === 0 && (
        <p className="settings-models-catalog-empty">{t("settings.models.modelsEmpty")}</p>
      )}
      {models.map((model, index) => (
        <div key={index} className="settings-models-entry">
          <div className="settings-models-entry-row">
            <input
              className="settings-models-input"
              type="text"
              value={textOf(model, "id")}
              placeholder={t("settings.models.modelId")}
              aria-label={`${t("settings.models.modelId")} ${index + 1}`}
              disabled={disabled}
              onChange={(event) => { patch(index, { id: event.target.value }); }}
            />
            <input
              className="settings-models-input"
              type="text"
              value={textOf(model, "name")}
              placeholder={t("settings.models.modelName")}
              aria-label={`${t("settings.models.modelName")} ${index + 1}`}
              disabled={disabled}
              onChange={(event) => { patch(index, { name: event.target.value === "" ? undefined : event.target.value }); }}
            />
            <button
              type="button"
              className="settings-models-icon"
              aria-label={`${t("settings.models.modelAdvanced")} ${index + 1}`}
              aria-expanded={expanded.has(index)}
              title={t("settings.models.modelAdvanced")}
              onClick={() => { toggleExpanded(index); }}
            >
              <IconChevron open={expanded.has(index)} />
            </button>
            <button
              type="button"
              className="settings-models-icon settings-models-icon-danger"
              aria-label={`${t("settings.models.removeModel")} ${index + 1}`}
              title={t("settings.models.removeModel")}
              disabled={disabled}
              onClick={() => { remove(index); }}
            >
              <IconTrash />
            </button>
          </div>
          {expanded.has(index) && (
            <div className="settings-models-entry-advanced">
              <label className="settings-models-entry-field">
                <span className="settings-models-entry-field-label">{t("settings.models.contextWindow")}</span>
                <input
                  className="settings-models-input"
                  type="text"
                  inputMode="numeric"
                  value={capacityText(model, index, "contextWindow")}
                  placeholder="256K"
                  aria-label={`${t("settings.models.contextWindow")} ${index + 1}`}
                  disabled={disabled}
                  onChange={(event) => { editCapacity(index, "contextWindow", event.target.value); }}
                />
              </label>
              <label className="settings-models-entry-field">
                <span className="settings-models-entry-field-label">{t("settings.models.maxTokens")}</span>
                <input
                  className="settings-models-input"
                  type="text"
                  inputMode="numeric"
                  value={capacityText(model, index, "maxTokens")}
                  placeholder="32K"
                  aria-label={`${t("settings.models.maxTokens")} ${index + 1}`}
                  disabled={disabled}
                  onChange={(event) => { editCapacity(index, "maxTokens", event.target.value); }}
                />
              </label>
            </div>
          )}
        </div>
      ))}
      <button
        type="button"
        className="settings-models-add-model"
        disabled={disabled}
        onClick={() => { onChange([...models, { id: "" }]); }}
      >
        <IconPlus />
        {t("settings.models.addModel")}
      </button>
    </section>
  );
}
