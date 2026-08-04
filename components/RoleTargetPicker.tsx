"use client";

import { Bot, Check, ChevronDown, Search, UserRound } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { RoleSummary } from "@/components/settings/types";

export function RoleTargetPicker({ roles, selectedRoleId, disabled, onSelect }: {
  roles: RoleSummary[];
  selectedRoleId: string | null;
  disabled?: boolean;
  onSelect: (roleId: string | null) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const selected = roles.find((role) => role.id === selectedRoleId) ?? null;
  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return roles;
    return roles.filter((role) => [role.name, role.handle, role.positioning].some((value) => value.toLowerCase().includes(needle)));
  }, [query, roles]);

  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [open]);

  function choose(roleId: string | null) {
    onSelect(roleId);
    setOpen(false);
    setQuery("");
  }

  return <div className="role-target-picker" ref={root}>
    <button type="button" className="role-target-trigger" disabled={disabled} aria-haspopup="listbox" aria-expanded={open}
      onClick={() => setOpen((value) => !value)} title="Invocation target">
      {selected ? <Bot size={13} aria-hidden="true" /> : <UserRound size={13} aria-hidden="true" />}
      <span>{selected ? `@${selected.handle}` : "Host"}</span><ChevronDown size={12} aria-hidden="true" />
    </button>
    {open && <div className="role-target-menu">
      <label><Search size={13} aria-hidden="true" /><input autoFocus value={query} placeholder="Find a Role"
        onChange={(event) => setQuery(event.target.value)} /></label>
      <div role="listbox" aria-label="Invocation target">
        <button type="button" role="option" aria-selected={!selected} onClick={() => choose(null)}>
          <UserRound size={14} /><span><strong>Host</strong><small>Default assistant</small></span>{!selected && <Check size={13} />}
        </button>
        {visible.map((role) => <button type="button" role="option" aria-selected={role.id === selectedRoleId}
          key={role.id} onClick={() => choose(role.id)}>
          <span className="role-target-swatch" style={{ color: role.color }}><Bot size={14} /></span>
          <span><strong>@{role.handle}</strong><small>{role.positioning || role.name}</small></span>
          {role.id === selectedRoleId && <Check size={13} />}
        </button>)}
        {visible.length === 0 && <span className="role-target-empty">No matching Roles</span>}
      </div>
    </div>}
  </div>;
}
