"use client";

import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";

interface PromptTemplateItem {
  name: string;
  description: string;
  argumentHint: string;
}

interface PromptCommandMenuProps {
  templates: PromptTemplateItem[];
  input: string;
  onSelect: (name: string) => void;
  onClose: () => void;
}

export function PromptCommandMenu({ templates, input, onSelect, onClose }: PromptCommandMenuProps) {
  const [activeIndex, setActiveIndex] = useState(0);
  const listRef = useRef<HTMLUListElement>(null);
  const mountedAt = useRef(0);

  useEffect(() => { mountedAt.current = Date.now(); }, []);

  const token = (() => {
    if (!input.startsWith("/")) return "";
    const spaceIdx = input.indexOf(" ");
    if (spaceIdx < 0) return input.slice(1);
    return input.slice(1, spaceIdx);
  })();

  const filtered = (() => {
    if (!token) return templates;
    const lower = token.toLowerCase();
    const prefix: PromptTemplateItem[] = [];
    const desc: PromptTemplateItem[] = [];
    for (const t of templates) {
      if (t.name.toLowerCase().startsWith(lower)) {
        prefix.push(t);
      } else if (t.description.toLowerCase().includes(lower)) {
        desc.push(t);
      }
    }
    return [...prefix, ...desc];
  })();

  // Reset active index when filter changes.
  const effectiveActive = activeIndex >= filtered.length && filtered.length > 0 ? 0 : activeIndex;

  // Close on outside click.
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (Date.now() - mountedAt.current < 100) return;
      if (!(e.target as HTMLElement).closest(".prompt-command-menu")) onClose();
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [onClose]);

  // Close on Escape.
  useEffect(() => {
    const handler = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") { onClose(); e.preventDefault(); }
    };
    document.addEventListener("keydown", handler, true);
    return () => document.removeEventListener("keydown", handler, true);
  }, [onClose]);

  const handleKeyDown = useCallback((e: KeyboardEvent<HTMLUListElement>) => {
    switch (e.key) {
      case "ArrowDown": e.preventDefault(); setActiveIndex((p) => Math.min(p + 1, filtered.length - 1)); break;
      case "ArrowUp": e.preventDefault(); setActiveIndex((p) => Math.max(p - 1, 0)); break;
      case "Enter": case "Tab": e.preventDefault(); if (filtered[effectiveActive]) onSelect(filtered[effectiveActive].name); break;
    }
  }, [filtered, effectiveActive, onSelect]);

  useEffect(() => {
    (listRef.current?.children[effectiveActive] as HTMLElement | undefined)?.scrollIntoView({ block: "nearest" });
  }, [effectiveActive]);

  if (filtered.length === 0) return null;

  return (
    <div className="prompt-command-menu" role="listbox" aria-label="Prompt templates">
      <ul ref={listRef} onKeyDown={handleKeyDown}>
        {filtered.map((tpl, idx) => (
          <li key={tpl.name} role="option" aria-selected={idx === effectiveActive}
            className={idx === effectiveActive ? "active" : ""}
            onMouseEnter={() => setActiveIndex(idx)}
            onMouseDown={(e) => { e.preventDefault(); onSelect(tpl.name); }}>
            <span className="pcm-name">/{tpl.name}</span>
            {tpl.argumentHint && <span className="pcm-hint">{tpl.argumentHint}</span>}
            <span className="pcm-desc">{tpl.description}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
