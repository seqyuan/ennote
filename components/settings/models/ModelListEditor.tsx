"use client";

import { ChevronDown, ChevronRight, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { formatCapacity, parseCapacity } from "@/lib/capacity";
import type { ModelDraft } from "@/lib/model-draft";

/** The two token counts edited as K/M-suffixed text behind a row's disclosure. */
type CapacityField = "contextWindow" | "maxTokens";

/** A row's text field, or the empty string when unset or not a string. */
function textOf(model: ModelDraft, key: string): string {
  const value = model[key];
  return typeof value === "string" ? value : "";
}

/** A row's numeric field, or `undefined` when unset or not a number. */
function numberOf(model: ModelDraft, key: string): number | undefined {
  const value = model[key];
  return typeof value === "number" ? value : undefined;
}

/**
 * The model list of one provider profile: one row per model (id + display
 * name) with the context window and output cap behind a per-row disclosure,
 * plus the add/delete/reset actions. An empty list means "serve the built-in
 * catalog" once Phase 2 lands; for now the parent decides what an empty list
 * means.
 */
export function ModelListEditor({ models, onChange, overridden, onReset, disabled }: {
  models: readonly ModelDraft[];
  onChange: (models: ModelDraft[]) => void;
  /** Whether the user layer currently owns the whole array (shows Reset). */
  overridden?: boolean;
  /** Remove the user-owned array and return to inheritance. */
  onReset?: () => void;
  disabled?: boolean;
}) {
  const t = useT();
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(() => new Set());
  // Capacities are edited as text, so a field's keystrokes are held here
  // rather than re-derived from the parsed count on every change (which would
  // rewrite `1000` to `1K` mid-word). One entry per field: a single buffer
  // would be displaced by editing any other field.
  const [editing, setEditing] = useState<ReadonlyMap<string, string>>(() => new Map());

  const bufferKey = (index: number, field: CapacityField): string => `${String(index)}:${field}`;

  const patch = (index: number, next: Record<string, string | number | undefined>): void => {
    onChange(models.map((model, at) => {
      if (at !== index) return model;
      // Rebuilt rather than spread over: an emptied optional field must leave
      // the profile, not be stored as a value its schema would reject.
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

  const inputStyle: React.CSSProperties = {
    height: 30, padding: "0 8px", border: "1px solid var(--stg-border-l2)",
    borderRadius: 6, background: "var(--stg-input-fill)", color: "var(--stg-text-primary)",
    font: "13px/20px var(--font-sans)", minWidth: 0,
  };
  const iconButtonStyle: React.CSSProperties = {
    display: "grid", placeItems: "center", width: 28, height: 28, padding: 0, flexShrink: 0,
    border: "1px solid var(--stg-border-l2)", borderRadius: 6, background: "transparent",
    color: "var(--stg-text-secondary)", cursor: "pointer",
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <span style={{ fontSize: 12, fontWeight: 500, color: "var(--stg-text-primary)" }}>{t("settings.models.models")}</span>
        {overridden !== undefined && (
          <span style={{ fontSize: 11, color: "var(--stg-text-tertiary)" }}>
            {overridden ? t("settings.models.modelsCustomized") : t("settings.models.modelsInherited")}
          </span>
        )}
        {overridden === true && onReset !== undefined && (
          <button
            type="button"
            className="secondary-btn"
            style={{ minHeight: 26, height: 26, padding: "0 10px", borderRadius: 13, fontSize: 11, marginLeft: "auto" }}
            disabled={disabled}
            onClick={onReset}
          >
            {t("settings.models.resetModels")}
          </button>
        )}
      </div>
      {models.length === 0 && (
        <p style={{ margin: 0, fontSize: 12, lineHeight: "18px", color: "var(--stg-text-tertiary)" }}>
          {t("settings.models.modelsEmpty")}
        </p>
      )}
      {models.map((model, index) => (
        <div key={index} style={{ display: "flex", flexDirection: "column", gap: 4, padding: "6px 0" }}>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <input
              type="text"
              value={textOf(model, "id")}
              placeholder={t("settings.models.modelId")}
              aria-label={`${t("settings.models.modelId")} ${index + 1}`}
              disabled={disabled}
              style={{ ...inputStyle, flex: 1 }}
              onChange={(event) => { patch(index, { id: event.target.value }); }}
            />
            <input
              type="text"
              value={textOf(model, "name")}
              placeholder={t("settings.models.modelName")}
              aria-label={`${t("settings.models.modelName")} ${index + 1}`}
              disabled={disabled}
              style={{ ...inputStyle, flex: 1 }}
              onChange={(event) => { patch(index, { name: event.target.value === "" ? undefined : event.target.value }); }}
            />
            <button
              type="button"
              aria-label={`${t("settings.models.modelAdvanced")} ${index + 1}`}
              aria-expanded={expanded.has(index)}
              title={t("settings.models.modelAdvanced")}
              style={iconButtonStyle}
              onClick={() => { toggleExpanded(index); }}
            >
              {expanded.has(index) ? <ChevronDown size={13} aria-hidden="true" /> : <ChevronRight size={13} aria-hidden="true" />}
            </button>
            <button
              type="button"
              aria-label={`${t("settings.models.removeModel")} ${index + 1}`}
              title={t("settings.models.removeModel")}
              disabled={disabled}
              style={{ ...iconButtonStyle, color: "var(--stg-danger)" }}
              onClick={() => { remove(index); }}
            >
              <Trash2 size={13} aria-hidden="true" />
            </button>
          </div>
          {expanded.has(index) && (
            <div style={{ display: "flex", gap: 8, paddingLeft: 2 }}>
              <label style={{ display: "flex", flexDirection: "column", gap: 3, flex: 1, fontSize: 11, color: "var(--stg-text-secondary)" }}>
                {t("settings.models.contextWindow")}
                <input
                  type="text"
                  inputMode="numeric"
                  value={capacityText(model, index, "contextWindow")}
                  placeholder="256K"
                  aria-label={`${t("settings.models.contextWindow")} ${index + 1}`}
                  disabled={disabled}
                  style={inputStyle}
                  onChange={(event) => { editCapacity(index, "contextWindow", event.target.value); }}
                />
              </label>
              <label style={{ display: "flex", flexDirection: "column", gap: 3, flex: 1, fontSize: 11, color: "var(--stg-text-secondary)" }}>
                {t("settings.models.maxTokens")}
                <input
                  type="text"
                  inputMode="numeric"
                  value={capacityText(model, index, "maxTokens")}
                  placeholder="32K"
                  aria-label={`${t("settings.models.maxTokens")} ${index + 1}`}
                  disabled={disabled}
                  style={inputStyle}
                  onChange={(event) => { editCapacity(index, "maxTokens", event.target.value); }}
                />
              </label>
            </div>
          )}
        </div>
      ))}
      <button
        type="button"
        className="secondary-btn"
        style={{ alignSelf: "flex-start", minHeight: 28, height: 28, padding: "0 12px", borderRadius: 14, fontSize: 12, display: "inline-flex", alignItems: "center", gap: 5 }}
        disabled={disabled}
        onClick={() => { onChange([...models, { id: "" }]); }}
      >
        <Plus size={13} aria-hidden="true" />
        {t("settings.models.addModel")}
      </button>
    </div>
  );
}
