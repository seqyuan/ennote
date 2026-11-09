"use client";

import { ChevronDown, ChevronRight, Search } from "lucide-react";
import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { useT } from "@/components/LocaleProvider";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";

export interface PickerItem {
  key: string;
  label: string;
  /** Right-aligned qualifier: scope, version, context mode. */
  hint?: string;
  description?: string;
  icon?: ReactNode;
  disabled?: boolean;
}

export interface PickerSection {
  key: string;
  label: string;
  items: PickerItem[];
  /** Item count before collapsing, so a folded group still states its size. */
  total?: number;
  /** Actions are pinned: always visible, never filtered, never collapsed. */
  pinned?: boolean;
  /** A read-only group explains items the picker deliberately does not offer. */
  readOnly?: boolean;
}

const COLLAPSED_KEY = "ennote-picker-collapsed";

function persistCollapsed(value: Record<string, boolean>): void {
  try { localStorage.setItem(COLLAPSED_KEY, JSON.stringify(value)); } catch { /* storage unavailable */ }
}

function readCollapsed(): Record<string, boolean> {
  if (typeof window === "undefined") return {};
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? "{}");
    return typeof parsed === "object" && parsed !== null ? parsed as Record<string, boolean> : {};
  } catch {
    return {};
  }
}

/**
 * The composer's one picker: a vertical, grouped, collapsible list shared by `@`,
 * `/`, and the `+` button.
 *
 * It opens upward because it is anchored to the composer's bottom edge; group
 * headers stick so a long Skills list does not lose its section titles, and the
 * body scrolls inside a bounded height so the panel can never push the composer
 * around. Filtering is the caller's text when the picker was opened from a token
 * (`@sec`) and this component's own input when it was opened from `+`.
 */
export interface ComposerPickerHandle {
  /** Move the cursor by one addressable row. */
  move: (delta: number) => void;
  /** Pick the row the cursor is on. Returns false when there is nothing to pick. */
  pickActive: () => boolean;
}

export const ComposerPicker = forwardRef<ComposerPickerHandle, {
  open: boolean;
  sections: PickerSection[];
  filter: string;
  showFilter?: boolean;
  activeKey?: string | null;
  ariaLabel: string;
  onFilterChange?: (value: string) => void;
  onPick: (item: PickerItem, section: PickerSection) => void;
  onClose: () => void;
}>(function ComposerPicker({
  open, sections, filter, showFilter = false, activeKey, ariaLabel, onFilterChange, onPick, onClose,
}, ref) {
  const t = useT();
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(readCollapsed);
  const [active, setActive] = useState(0);
  const rootRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const filterRef = useRef<HTMLInputElement>(null);

  const visible = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    return sections
      .map((section) => {
        if (section.pinned) return { ...section, total: section.items.length };
        // A read-only group explains rather than offers, so it starts folded.
        const isCollapsed = collapsed[section.key] ?? section.readOnly ?? false;
        const items = needle
          ? section.items.filter((item) => `${item.label} ${item.hint ?? ""} ${item.description ?? ""}`
            .toLowerCase().includes(needle))
          : section.items;
        return { ...section, items: isCollapsed ? [] : items, total: items.length };
      })
      .filter((section) => section.pinned || section.items.length > 0 || section.readOnly);
  }, [sections, collapsed, filter]);

  // Pinned actions are shortcuts, not the content the panel was opened for: the
  // cursor starts on the first addressable row, so Enter never opens a file
  // dialog when the user typed a query.
  const selectable = useMemo(
    () => visible.flatMap((section) => section.items
      .filter((item) => !section.pinned && !section.readOnly && !item.disabled)
      .map((item) => ({ item, section }))),
    [visible],
  );

  // Reset the cursor when the filter or the panel identity changes. Deriving it
  // from a key instead of an effect keeps the reset out of the render loop.
  const listKey = `${filter}\u0000${open}`;
  const [cursorKey, setCursorKey] = useState(listKey);
  if (cursorKey !== listKey) {
    setCursorKey(listKey);
    setActive(0);
  }
  useEffect(() => {
    if (!open) return;
    if (showFilter) filterRef.current?.focus();
    else rootRef.current?.querySelector<HTMLElement>("[data-picker-item][data-active='true']")?.scrollIntoView({ block: "nearest" });
  }, [open, showFilter, active]);

  useOverlayLayer(open, onClose);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (event.target instanceof Node && rootRef.current?.contains(event.target) === true) return;
      onClose();
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open, onClose]);

  const toggle = useCallback((key: string, currentlyCollapsed: boolean) => {
    setCollapsed((previous) => {
      const next = { ...previous, [key]: !currentlyCollapsed };
      persistCollapsed(next);
      return next;
    });
  }, []);

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActive((index) => Math.min(index + 1, Math.max(selectable.length - 1, 0)));
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive((index) => Math.max(index - 1, 0));
      return;
    }
    if (event.key === "Enter") {
      const picked = selectable[active];
      if (!picked) return;
      event.preventDefault();
      onPick(picked.item, picked.section);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      onClose();
    }
  };

  // A token filter that matches nothing closes the panel: leaving only the pinned
  // Actions group visible would answer "@zzz" with "attach a file".
  useImperativeHandle(ref, () => ({
    move: (delta: number) => setActive((index) => {
      const next = index + delta;
      if (next < 0) return 0;
      return Math.min(next, Math.max(selectable.length - 1, 0));
    }),
    pickActive: () => {
      const picked = selectable[Math.min(active, Math.max(selectable.length - 1, 0))];
      if (!picked) return false;
      onPick(picked.item, picked.section);
      return true;
    },
  }), [selectable, active, onPick]);

  if (!open) return null;
  const filteredMatch = visible.some((section) => !section.pinned && section.items.length > 0);
  if (!showFilter && filter.trim().length > 0 && !filteredMatch) return null;

  return (
    <div ref={rootRef} className="composer-picker" role="dialog" aria-label={ariaLabel} onKeyDown={onKeyDown}>
      {showFilter && (
        <label className="composer-picker-filter">
          <Search size={13} aria-hidden="true" />
          <input
            ref={filterRef}
            value={filter}
            placeholder={t("picker.filter")}
            aria-label={t("picker.filter")}
            onChange={(event) => onFilterChange?.(event.target.value)}
          />
        </label>
      )}
      <div
        ref={listRef}
        className="composer-picker-body"
        role="listbox"
        aria-label={t("picker.list")}
        aria-activedescendant={selectable[active] ? `picker-${selectable[active].item.key}` : undefined}
      >
        {visible.map((section) => {
          // A read-only group explains rather than offers, so it starts folded.
        const isCollapsed = collapsed[section.key] ?? section.readOnly ?? false;
          const collapsible = !section.pinned;
          return (
            <section key={section.key} className="composer-picker-group" role="group" aria-label={section.label}>
              <button
                type="button"
                className="composer-picker-group-head"
                aria-expanded={collapsible ? !isCollapsed : undefined}
                disabled={!collapsible}
                onClick={() => collapsible && toggle(section.key, isCollapsed)}
                onKeyDown={(event) => {
                  if (event.key !== "Enter" && event.key !== " ") return;
                  event.preventDefault();
                  event.stopPropagation();
                  if (collapsible) toggle(section.key, isCollapsed);
                }}
                title={collapsible ? t(isCollapsed ? "picker.expand" : "picker.collapse") : undefined}
              >
                {collapsible
                  ? (isCollapsed ? <ChevronRight size={12} aria-hidden="true" /> : <ChevronDown size={12} aria-hidden="true" />)
                  : <span className="composer-picker-group-dot" aria-hidden="true" />}
                <span>{section.label}</span>
                <span className="composer-picker-group-count">{section.total}</span>
                {section.readOnly && <span className="composer-picker-group-tag">{t("picker.readOnly")}</span>}
              </button>
              {section.items.map((item) => {
                const isActive = section.readOnly ? false : selectable[active]?.item.key === item.key;
                return (
                  <button
                    key={item.key}
                    id={`picker-${item.key}`}
                    type="button"
                    role="option"
                    data-picker-item
                    data-active={isActive || undefined}
                    aria-selected={isActive}
                    aria-disabled={section.readOnly || item.disabled || undefined}
                    className={`composer-picker-item${isActive ? " active" : ""}${section.pinned ? " is-action" : ""}`}
                    onClick={() => { if (!section.readOnly && !item.disabled) onPick(item, section); }}
                  >
                    <span className="composer-picker-item-icon" aria-hidden="true">{item.icon}</span>
                    <span className="composer-picker-item-copy">
                      <span className="composer-picker-item-label">{item.label}</span>
                      {item.description && <span className="composer-picker-item-desc">{item.description}</span>}
                    </span>
                    {item.hint && <span className="composer-picker-item-hint">{item.hint}</span>}
                  </button>
                );
              })}
            </section>
          );
        })}
        {selectable.length === 0 && <div className="composer-picker-empty">{t("picker.empty")}</div>}
      </div>
      <span className="sr-only" aria-live="polite">{activeKey ?? selectable[active]?.item.label ?? ""}</span>
    </div>
  );
});
