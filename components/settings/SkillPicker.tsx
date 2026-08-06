"use client";

import { Search } from "lucide-react";
import { useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { AnnotatedSkill, SkillListResult } from "@/components/settings/types";

export interface RoleSkillBinding {
  skillId: string;
  mode: "preload" | "available";
}

// SkillPicker is the Role binding surface: it lists the effective skill catalog
// (user + builtin roots) and lets the editor attach skills by their manifest ID
// with a preload/available mode. Bindings write RoleDefinition.skills.entries.
export function SkillPicker({ entries, onEntries }: {
  entries: RoleSkillBinding[];
  onEntries: (entries: RoleSkillBinding[]) => void;
}) {
  const [catalog, setCatalog] = useState<AnnotatedSkill[]>([]);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);

  useEffect(() => {
    const t0 = window.setTimeout(() => {
      void apiFetch<SkillListResult>("/v1/skills")
        .then((result) => {
          setCatalog((result.skills ?? []).filter((skill) => Boolean(skill.skillId)));
          setLoadError(false);
        })
        .catch(() => setLoadError(true))
        .finally(() => setLoading(false));
    }, 0);
    return () => window.clearTimeout(t0);
  }, []);

  const selected = new Map(entries.map((entry) => [entry.skillId, entry.mode]));
  const needle = query.trim().toLowerCase();
  const visible = catalog
    .filter((skill) => !needle || skill.name.toLowerCase().includes(needle) ||
      (skill.description ?? "").toLowerCase().includes(needle))
    .sort((a, b) => {
      const aSelected = selected.has(a.skillId) ? 0 : 1;
      const bSelected = selected.has(b.skillId) ? 0 : 1;
      return aSelected - bSelected || a.name.localeCompare(b.name);
    });

  const toggle = (skillId: string) => {
    if (selected.has(skillId)) {
      onEntries(entries.filter((entry) => entry.skillId !== skillId));
    } else {
      onEntries([...entries, { skillId, mode: "available" }]);
    }
  };

  const setMode = (skillId: string, mode: "preload" | "available") => {
    onEntries(entries.map((entry) => entry.skillId === skillId ? { ...entry, mode } : entry));
  };

  return (
    <div className="role-skill-picker">
      <label className="role-skill-search">
        <Search size={13} aria-hidden="true" />
        <span className="sr-only">Search skills</span>
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search skills…"
        />
      </label>
      <div className="role-skill-count">
        {entries.length} bound · {catalog.length} in catalog
      </div>
      {loadError && (
        <div className="role-skill-error" role="alert">
          Skill catalog unavailable — publish validation will reject unknown skill IDs.
        </div>
      )}
      {loading && <div className="role-skill-empty">Loading catalog…</div>}
      {!loading && !loadError && visible.length === 0 && (
        <div className="role-skill-empty">No skills match “{query.trim()}”.</div>
      )}
      <div className="role-skill-list" role="listbox" aria-label="Skill bindings" aria-multiselectable="true">
        {visible.map((skill) => {
          const boundMode = selected.get(skill.skillId);
          return (
            <div key={skill.skillId} className={`role-skill-row${boundMode ? " is-bound" : ""}`} role="option" aria-selected={Boolean(boundMode)}>
              <label className="role-skill-check">
                <input
                  type="checkbox"
                  checked={Boolean(boundMode)}
                  onChange={() => toggle(skill.skillId)}
                />
                <span className="role-skill-name">{skill.name}</span>
              </label>
              {skill.description && (
                <span className="role-skill-desc" title={skill.description}>{skill.description}</span>
              )}
              <span className="role-skill-source">{skill.sourceInfo?.scope ?? "catalog"}</span>
              {boundMode && (
                <select
                  value={boundMode}
                  onChange={(event) => setMode(skill.skillId, event.target.value as "preload" | "available")}
                  aria-label={`Mode for ${skill.name}`}
                  className="role-skill-mode"
                >
                  <option value="preload">Preload</option>
                  <option value="available">Available</option>
                </select>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
