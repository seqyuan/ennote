"use client";

import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";

interface PromptTemplateItem {
  name: string;
  description: string;
  argumentHint: string;
}

export interface RoleMenuItem {
  id: string;
  handle: string;
  name: string;
  description?: string;
}

export interface FlowMenuItem {
  name: string;
  version?: number;
  description?: string;
}

interface PromptCommandMenuProps {
  templates: PromptTemplateItem[];
  roles: RoleMenuItem[];
  flows: FlowMenuItem[];
  input: string;
  onSelectTemplate: (name: string) => void;
  onSelectRole: (roleId: string, handle: string) => void;
  onSelectFlow: (name: string, version?: number) => void;
  onClose: () => void;
}

type Mode = "template" | "role" | "flow" | "all";

interface MenuEntry {
  key: string;
  label: string;
  hint?: string;
  desc?: string;
  onPick: () => void;
}

function activeMode(input: string): Mode | null {
  if (input.startsWith("/")) return "template";
  if (input.startsWith("@role")) return "role";
  if (input.startsWith("@graph")) return "flow";
  if (input.startsWith("@")) return "all";
  return null;
}

// tokenAfter strips the "@kind:" or "@kind" prefix (both spellings) and
// returns the search token up to the first space.
function tokenAfter(input: string, prefix: string): string {
  const withoutColon = input.startsWith(`${prefix}:`) ? input.slice(prefix.length + 1) : input.slice(prefix.length);
  return withoutColon.split(/\s/)[0].toLowerCase();
}

export function PromptCommandMenu({
  templates, roles, flows, input, onSelectTemplate, onSelectRole, onSelectFlow, onClose,
}: PromptCommandMenuProps) {
  const [activeIndex, setActiveIndex] = useState(0);
  const listRef = useRef<HTMLUListElement>(null);
  const mountedAt = useRef(0);

  useEffect(() => { mountedAt.current = Date.now(); }, []);

  const mode = activeMode(input);

  const entries: MenuEntry[] = (() => {
    if (mode === "template") {
      const token = input.slice(1).split(/\s/)[0].toLowerCase();
      const filtered = templates
        .filter((tpl) => !token || tpl.name.toLowerCase().includes(token) ||
          tpl.description.toLowerCase().includes(token));
      return filtered.map((tpl) => ({
        key: `t:${tpl.name}`,
        label: `/${tpl.name}`,
        hint: tpl.argumentHint,
        desc: tpl.description,
        onPick: () => onSelectTemplate(tpl.name),
      }));
    }
    if (mode === "role") {
      const token = tokenAfter(input, "@role");
      const filtered = roles
        .filter((role) => !token || role.handle.toLowerCase().includes(token) ||
          role.name.toLowerCase().includes(token));
      return filtered.map((role) => ({
        key: `r:${role.id}`,
        label: `@role:${role.handle}`,
        hint: role.name,
        desc: role.description,
        onPick: () => onSelectRole(role.id, role.handle),
      }));
    }
    if (mode === "flow") {
      const token = tokenAfter(input, "@graph");
      const filtered = flows
        .filter((flow) => !token || flow.name.toLowerCase().includes(token));
      return filtered.map((flow) => ({
        key: `f:${flow.name}`,
        label: `@graph:${flow.name}${flow.version ? `@${flow.version}` : ""}`,
        hint: flow.version ? `v${flow.version}` : "",
        desc: flow.description,
        onPick: () => onSelectFlow(flow.name, flow.version),
      }));
    }
    if (mode === "all") {
      const token = input.slice(1).split(/\s/)[0].toLowerCase();
      const filteredRoles = roles
        .filter((role) => !token || role.handle.toLowerCase().includes(token) || role.name.toLowerCase().includes(token));
      const filteredFlows = flows.filter((flow) => !token || flow.name.toLowerCase().includes(token));
      return [
        ...filteredRoles.map((role) => ({
          key: `r:${role.id}`,
          label: `@role:${role.handle}`,
          hint: role.name,
          desc: role.description,
          onPick: () => onSelectRole(role.id, role.handle),
        })),
        ...filteredFlows.map((flow) => ({
          key: `f:${flow.name}`,
          label: `@graph:${flow.name}${flow.version ? `@${flow.version}` : ""}`,
          hint: flow.version ? `v${flow.version}` : "",
          desc: flow.description,
          onPick: () => onSelectFlow(flow.name, flow.version),
        })),
      ];
    }
    return [];
  })();

  // Reset active index when filter changes.
  const effectiveActive = activeIndex >= entries.length && entries.length > 0 ? 0 : activeIndex;

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
      case "ArrowDown": e.preventDefault(); setActiveIndex((p) => Math.min(p + 1, entries.length - 1)); break;
      case "ArrowUp": e.preventDefault(); setActiveIndex((p) => Math.max(p - 1, 0)); break;
      case "Enter": case "Tab": e.preventDefault(); if (entries[effectiveActive]) entries[effectiveActive].onPick(); break;
    }
  }, [entries, effectiveActive]);

  useEffect(() => {
    (listRef.current?.children[effectiveActive] as HTMLElement | undefined)?.scrollIntoView({ block: "nearest" });
  }, [effectiveActive]);

  if (entries.length === 0 || !mode) return null;

  const ariaLabel = mode === "template" ? "Prompt templates"
    : mode === "role" ? "Role targets" : "Agent Flows";

  return (
    <div className="prompt-command-menu" role="listbox" aria-label={ariaLabel}>
      <ul ref={listRef} onKeyDown={handleKeyDown}>
        {entries.map((entry, idx) => (
          <li key={entry.key} role="option" aria-selected={idx === effectiveActive}
            className={idx === effectiveActive ? "active" : ""}
            onMouseEnter={() => setActiveIndex(idx)}
            onMouseDown={(e) => { e.preventDefault(); entry.onPick(); }}>
            <span className="pcm-name">{entry.label}</span>
            {entry.hint && <span className="pcm-hint">{entry.hint}</span>}
            {entry.desc && <span className="pcm-desc">{entry.desc}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}
