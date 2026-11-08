"use client";

import { Bot, Check, ChevronDown, Search, UserRound } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";
import type { HeroSummary } from "@/components/settings/types";

export function HeroTargetPicker({ heroes, selectedHeroId, disabled, onSelect }: {
  heroes: HeroSummary[];
  selectedHeroId: string | null;
  disabled?: boolean;
  onSelect: (heroId: string | null) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const selected = heroes.find((hero) => hero.id === selectedHeroId) ?? null;
  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return heroes;
    return heroes.filter((hero) => [hero.name, hero.handle, hero.positioning].some((value) => value.toLowerCase().includes(needle)));
  }, [query, heroes]);

  useOverlayLayer(open, () => setOpen(false));

  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [open]);

  function choose(heroId: string | null) {
    onSelect(heroId);
    setOpen(false);
    setQuery("");
    triggerRef.current?.focus();
  }

  function onMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const options = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="option"]') ?? []);
    if (options.length === 0) return;
    event.preventDefault();
    const at = options.indexOf(document.activeElement as HTMLElement);
    const next = (Math.max(at, 0) + (event.key === "ArrowDown" ? 1 : -1) + options.length) % options.length;
    options[next]?.focus();
  }

  return <div className="hero-target-picker" ref={root}>
    <button ref={triggerRef} type="button" className="hero-target-trigger" disabled={disabled} aria-haspopup="listbox" aria-expanded={open}
      onClick={() => setOpen((value) => !value)} title="Invocation target">
      {selected ? <Bot size={13} aria-hidden="true" /> : <UserRound size={13} aria-hidden="true" />}
      <span>{selected ? `@${selected.handle}` : "Host"}</span><ChevronDown size={12} aria-hidden="true" />
    </button>
    {open && <div ref={menuRef} className="hero-target-menu" onKeyDown={onMenuKeyDown}>
      <label><Search size={13} aria-hidden="true" /><input autoFocus value={query} placeholder="Find a Hero"
        onChange={(event) => setQuery(event.target.value)} /></label>
      <div role="listbox" aria-label="Invocation target">
        <button type="button" role="option" aria-selected={!selected} onClick={() => choose(null)}>
          <UserRound size={14} /><span><strong>Host</strong><small>Default assistant</small></span>{!selected && <Check size={13} />}
        </button>
        {visible.map((hero) => <button type="button" role="option" aria-selected={hero.id === selectedHeroId}
          key={hero.id} onClick={() => choose(hero.id)}>
          <span className="hero-target-swatch" style={{ color: hero.color }}><Bot size={14} /></span>
          <span><strong>@{hero.handle}</strong><small>{hero.positioning || hero.name}</small></span>
          {hero.id === selectedHeroId && <Check size={13} />}
        </button>)}
        {visible.length === 0 && <span className="hero-target-empty">No matching Heroes</span>}
      </div>
    </div>}
  </div>;
}
