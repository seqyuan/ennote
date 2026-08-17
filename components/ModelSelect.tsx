"use client";

/**
 * ModelSelect: the composer's model + reasoning-effort seat, pixel-replicated
 * from deepseek-harness's `ui-model-selection` (dsh ModelSelect.tsx /
 * ModelSelect.module.css).
 *
 * One pill trigger shows the model name with the effort beside it in the
 * caption tone and a chevron. It opens a two-level menu above the trigger:
 * the root is the Model / Effort row pair, each drilling into its own list —
 * the provider-grouped model list and the effort levels. The selected model's
 * `supportsThinking` gates the effort row, matching dsh's reasoning metadata.
 */
import { useEffect, useId, useMemo, useRef, useState, type FocusEvent } from "react";
import { useT } from "@/components/LocaleProvider";
import type { ModelProfile } from "@/components/settings/types";
import type { ThinkingEffort } from "@/lib/permission-mode";

type Pane = "root" | "model" | "effort";

/** Simple {var} interpolation; ennote's translate has none. */
function fill(template: string, vars: Record<string, string>): string {
  return template.replace(/\{(\w+)\}/g, (_, name: string) => vars[name] ?? `{${name}}`);
}

/** dsh-style effort name: capitalize the level id ("default" → "Default"). */
function effortName(level: string): string {
  return level.charAt(0).toUpperCase() + level.slice(1);
}

function ChevronRightIcon() {
  return (
    <svg className="model-select-cell-chevron" width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M5.25 3.5L8.75 7L5.25 10.5" />
    </svg>
  );
}

function CheckIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <polyline points="3 8.5 6.5 12 13 4.5" />
    </svg>
  );
}

export function ModelSelect({ models, selectedModelId, setSelectedModelId, thinkingEffort, setThinkingEffort, disabled }: {
  models: ModelProfile[];
  selectedModelId: string | null;
  setSelectedModelId: (modelId: string) => void;
  thinkingEffort: ThinkingEffort;
  setThinkingEffort: (effort: ThinkingEffort) => void;
  disabled: boolean;
}) {
  let itemIndex = 0;
  const t = useT();
  const [open, setOpen] = useState(false);
  const [pane, setPane] = useState<Pane>("root");
  const rootRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const id = useId();

  const selected = useMemo(
    () => models.find((model) => model.id === selectedModelId) ?? null,
    [models, selectedModelId],
  );

  // Provider-grouped model list, mirroring dsh's ModelDirectory groups.
  const groups = useMemo(() => {
    const byProvider = new Map<string, ModelProfile[]>();
    for (const model of models) {
      const list = byProvider.get(model.providerId) ?? [];
      list.push(model);
      byProvider.set(model.providerId, list);
    }
    return [...byProvider.entries()];
  }, [models]);

  const reasoning = selected?.supportsThinking ? selected : null;
  const efforts = useMemo<readonly ThinkingEffort[]>(() => {
    if (!reasoning) return [];
    const declared = reasoning.supportedThinkingEfforts;
    return declared && declared.length > 0 ? declared : ["default"];
  }, [reasoning]);

  const active: ThinkingEffort = efforts.length > 0 && efforts.includes(thinkingEffort) ? thinkingEffort : "default";
  const effortLabel = reasoning === null ? undefined : effortName(active);

  // Switching models resets an unsupported effort back to the default,
  // including a non-thinking model (whose offered list degrades to [default]).
  useEffect(() => {
    if (!selected) return;
    const list = selected.supportsThinking && selected.supportedThinkingEfforts?.length
      ? selected.supportedThinkingEfforts
      : (["default"] as ThinkingEffort[]);
    if (!list.includes(thinkingEffort)) setThinkingEffort("default");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedModelId]);

  const modelLabel = selected?.displayName || selected?.modelName || t("composer.modelSelect.triggerFallback");
  const triggerLabel = effortLabel === undefined ? modelLabel : `${modelLabel} · ${effortLabel}`;
  const triggerAria = selected === null
    ? t("composer.modelSelect.selectAria")
    : effortLabel === undefined
      ? fill(t("composer.modelSelect.aria"), { model: modelLabel })
      : fill(t("composer.modelSelect.ariaEffort"), { model: modelLabel, effort: effortLabel });

  const show = (): void => { setPane("root"); setOpen(true); };
  const close = (restoreFocus = false): void => {
    setOpen(false);
    setPane("root");
    if (restoreFocus) queueMicrotask(() => { triggerRef.current?.focus(); });
  };

  useEffect(() => {
    if (!open) return;
    const closeOutside = (event: MouseEvent): void => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", closeOutside);
    return () => { document.removeEventListener("mousedown", closeOutside); };
  }, [open]);

  const moveFocus = (offset: number): void => {
    const items = Array.from(rootRef.current?.querySelectorAll<HTMLElement>("[data-menu-index]") ?? []);
    if (items.length === 0) return;
    const at = items.findIndex((item) => item === document.activeElement);
    const next = (Math.max(at, 0) + offset + items.length) % items.length;
    items[next]?.focus();
  };

  // Window-level keys: pane switches unmount the focused item, so focus can
  // fall to <body>; Escape/arrows must still reach the menu.
  useEffect(() => {
    if (!open) return;
    const onKey = (event: globalThis.KeyboardEvent): void => {
      if (event.key === "Escape") {
        event.preventDefault();
        // Escape backs out of a drilled pane first, then closes.
        if (pane !== "root") setPane("root");
        else close(true);
        return;
      }
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        moveFocus(event.key === "ArrowDown" ? 1 : -1);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => { window.removeEventListener("keydown", onKey); };
  }, [open, pane]);

  const onBlur = (event: FocusEvent<HTMLDivElement>): void => {
    // A pane switch removes the focused item (relatedTarget null) — that must
    // not close the menu; the outside mousedown listener owns click-away.
    if (!(event.relatedTarget instanceof Node)) return;
    if (rootRef.current?.contains(event.relatedTarget)) return;
    close();
  };

  const choose = (model: ModelProfile): void => {
    if (selectedModelId === model.id) { close(true); return; }
    setSelectedModelId(model.id);
    close(true);
  };

  const chooseEffort = (effort: ThinkingEffort): void => {
    if (active === effort) { close(true); return; }
    setThinkingEffort(effort);
    close(true);
  };

  return (
    <div ref={rootRef} className="model-select" onBlur={onBlur}>
      <button
        ref={triggerRef}
        type="button"
        className="model-select-trigger"
        aria-label={triggerAria}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? `${id}-menu` : undefined}
        title={triggerLabel}
        disabled={disabled}
        onClick={() => { if (open) close(); else show(); }}
      >
        <span className="model-select-trigger-label">{modelLabel}</span>
        {effortLabel !== undefined && <span className="model-select-trigger-effort">{effortLabel}</span>}
        <svg className={open ? "model-select-chevron open" : "model-select-chevron"} width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M3.5 5.25L7 8.75L10.5 5.25" />
        </svg>
      </button>

      {open && (
        <div
          id={`${id}-menu`}
          className="model-select-menu"
          role="menu"
          aria-label={t("composer.modelSelect.menu")}
        >
          {pane === "root" && (
            <>
              <button data-menu-index={itemIndex++} type="button" role="menuitem" className="model-select-cell" onClick={() => { setPane("model"); }}>
                <span className="model-select-cell-label">{t("composer.modelSelect.model")}</span>
                <span className="model-select-cell-value">{modelLabel}</span>
                <ChevronRightIcon />
              </button>
              {reasoning !== null && (
                <button data-menu-index={itemIndex++} type="button" role="menuitem" className="model-select-cell" onClick={() => { setPane("effort"); }}>
                  <span className="model-select-cell-label">{t("composer.modelSelect.effort")}</span>
                  <span className="model-select-cell-value">{effortLabel}</span>
                  <ChevronRightIcon />
                </button>
              )}
            </>
          )}

          {pane === "model" && (
            <div className="model-select-groups">
              {groups.map(([providerId, providerModels]) => (
                <section role="group" aria-labelledby={`${id}-${providerId}`} className="model-select-group" key={providerId}>
                  <div className="model-select-group-title" id={`${id}-${providerId}`}>{providerId}</div>
                  {providerModels.map((model) => {
                    const isSelected = selectedModelId === model.id;
                    return (
                      <button
                        data-menu-index={itemIndex++}
                        type="button"
                        role="menuitemradio"
                        aria-checked={isSelected}
                        className={isSelected ? "model-select-option selected" : "model-select-option"}
                        key={model.id}
                        title={model.displayName || model.modelName}
                        onClick={() => { choose(model); }}
                      >
                        <span className="model-select-option-copy">
                          <span className="model-select-option-name">{model.displayName || model.modelName}</span>
                        </span>
                        <span className="model-select-check">
                          {isSelected && <CheckIcon />}
                        </span>
                      </button>
                    );
                  })}
                </section>
              ))}
              {models.length === 0 && <div className="model-select-empty">{t("composer.modelSelect.emptyModels")}</div>}
            </div>
          )}

          {pane === "effort" && (
            <>
              {efforts.map((level) => {
                const isActive = active === level;
                return (
                  <button
                    data-menu-index={itemIndex++}
                    type="button"
                    role="menuitemradio"
                    aria-checked={isActive}
                    className={isActive ? "model-select-option selected" : "model-select-option"}
                    key={level}
                    onClick={() => { chooseEffort(level); }}
                  >
                    <span className="model-select-option-copy">
                      <span className="model-select-option-name">{effortName(level)}</span>
                      <span className="model-select-option-desc">{t(`composer.thinking.${level}`)}</span>
                    </span>
                    <span className="model-select-check">
                      {isActive && <CheckIcon />}
                    </span>
                  </button>
                );
              })}
              {efforts.length === 0 && <div className="model-select-empty">{t("composer.modelSelect.emptyEfforts")}</div>}
            </>
          )}
        </div>
      )}
    </div>
  );
}
