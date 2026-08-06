"use client";

import { ExternalLink, RefreshCw, Search, X } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type {
  AnnotatedSkill,
  SkillInstallInfo,
  SkillListResult,
  SkillSearchResult,
  SkillUpdateResult,
} from "@/components/settings/types";

// SkillsSettings is the pi-web-style skills management surface: a marketplace
// search/install panel plus the installed catalog with enable/disable toggles,
// update checks, and removal. All operations run through the loopback Worker.
export function SkillsSettings({ projectId, setError }: {
  projectId: string | null;
  setError: (value: string | null) => void;
}) {
  const [skills, setSkills] = useState<AnnotatedSkill[]>([]);
  const [diagnostics, setDiagnostics] = useState<Array<{ level?: string; message?: string; relPath?: string; source?: string }>>([]);
  const [projectTrusted, setProjectTrusted] = useState(false);
  const [loading, setLoading] = useState(false);

  // Marketplace search.
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SkillSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const [searched, setSearched] = useState(false);
  const [installScope, setInstallScope] = useState<"global" | "project">("global");
  const [installing, setInstalling] = useState<string | null>(null);

  // Row actions.
  const [updates, setUpdates] = useState<Record<string, SkillUpdateResult>>({});
  const [checking, setChecking] = useState<string | null>(null);
  const [toggling, setToggling] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);

  const updateKey = (install: { package: string; scope: string }) => `${install.scope}\u0000${install.package}`;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const params = projectId ? `?projectID=${encodeURIComponent(projectId)}` : "";
      const result = await apiFetch<SkillListResult>(`/v1/skills${params}`);
      setSkills(result.skills ?? []);
      setDiagnostics(result.diagnostics ?? []);
      setProjectTrusted(Boolean(result.projectResourcesLoaded));
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Failed to load skills");
    } finally {
      setLoading(false);
    }
  }, [projectId, setError]);

  useEffect(() => {
    const t0 = window.setTimeout(() => void load(), 0);
    return () => window.clearTimeout(t0);
  }, [load]);

  const search = async () => {
    const q = query.trim();
    if (!q) return;
    setSearching(true);
    try {
      const data = await apiFetch<{ results: SkillSearchResult[] }>(
        `/v1/skills/search?q=${encodeURIComponent(q)}&limit=20`,
      );
      setResults(data.results ?? []);
      setSearched(true);
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Search failed");
    } finally {
      setSearching(false);
    }
  };

  const install = async (pkg: string) => {
    if (installScope === "project" && !projectTrusted) {
      setError("Project resources must be trusted before installing project skills.");
      return;
    }
    setInstalling(pkg);
    try {
      await apiFetch<{ success: boolean; output?: string }>("/v1/skills/install", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          package: pkg, scope: installScope,
          ...(installScope === "project" && projectId ? { projectId } : {}),
        }),
      });
      setError(null);
      setResults((current) => current.filter((item) => item.package !== pkg));
      await load();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Install failed");
    } finally {
      setInstalling(null);
    }
  };

  const check = async (install: SkillInstallInfo) => {
    const key = updateKey(install);
    setChecking(key);
    try {
      const data = await apiFetch<{ updates: SkillUpdateResult[] }>("/v1/skills/check", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          package: install.package, scope: install.scope,
          ...(install.scope === "project" && projectId ? { projectId } : {}),
        }),
      });
      setUpdates((current) => {
        const next = { ...current };
        for (const item of data.updates ?? []) next[updateKey(item)] = item;
        return next;
      });
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Update check failed");
    } finally {
      setChecking(null);
    }
  };

  const update = async (install: SkillInstallInfo) => {
    const key = updateKey(install);
    setChecking(key);
    try {
      await apiFetch<{ success: boolean; output?: string }>("/v1/skills/update", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          package: install.package, scope: install.scope,
          ...(install.scope === "project" && projectId ? { projectId } : {}),
        }),
      });
      setError(null);
      setUpdates((current) => {
        const next = { ...current };
        delete next[key];
        return next;
      });
      await load();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Update failed");
    } finally {
      setChecking(null);
    }
  };

  const toggle = async (skill: AnnotatedSkill) => {
    setToggling(skill.relPath);
    try {
      await apiFetch(`/v1/skills/disabled/${skill.relPath}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ disabled: !skill.disableModelInvocation }),
      });
      setSkills((current) => current.map((item) =>
        item.relPath === skill.relPath ? { ...item, disableModelInvocation: !skill.disableModelInvocation } : item));
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Toggle failed");
    } finally {
      setToggling(null);
    }
  };

  const remove = async (skill: AnnotatedSkill) => {
    if (!window.confirm(`Remove the skill "${skill.name}"?`)) return;
    setRemoving(skill.relPath);
    try {
      await apiFetch(`/v1/skills/remove/${skill.relPath}`, { method: "POST" });
      setSkills((current) => current.filter((item) => item.relPath !== skill.relPath));
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Remove failed");
    } finally {
      setRemoving(null);
    }
  };

  const installed = skills.filter((skill) => Boolean(skill.install));
  const local = skills.filter((skill) => !skill.install);

  return <section className="settings-tab-section" aria-labelledby="settings-skills-heading">
    <header>
      <h2 id="settings-skills-heading">Skills</h2>
      <p>Marketplace installs, catalog browsing, and per-skill invocation control.</p>
    </header>

    {/* Marketplace search */}
    <div className="skills-market-panel" style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 10, display: "flex", flexDirection: "column", gap: 8 }}>
      <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)" }}>Marketplace</div>
      <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => { if (event.key === "Enter") void search(); }}
          placeholder="Search skills.sh…"
          aria-label="Search skills.sh"
          style={inputStyle}
        />
        <select
          value={installScope}
          onChange={(event) => setInstallScope(event.target.value as "global" | "project")}
          aria-label="Install scope"
          style={{ ...inputStyle, width: 110 }}
        >
          <option value="global">Global</option>
          <option value="project" disabled={!projectId || !projectTrusted}>Project{!projectTrusted && projectId ? " (needs trust)" : ""}</option>
        </select>
        <button type="button" onClick={() => void search()} disabled={searching || !query.trim()}
          style={{ ...ghostButtonStyle, display: "inline-flex", alignItems: "center", gap: 5 }}>
          <Search size={13} aria-hidden="true" /> {searching ? "Searching…" : "Search"}
        </button>
      </div>
      {searched && results.length === 0 && !searching && (
        <div style={{ fontSize: 11, color: "var(--text-dim)" }}>No skills match &quot;{query.trim()}&quot;.</div>
      )}
      {results.map((result) => (
        <div key={result.package} style={{ display: "flex", alignItems: "center", gap: 8, padding: "6px 8px", border: "1px solid var(--border)", borderRadius: 6 }}>
          <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontFamily: "var(--font-mono, monospace)", fontSize: 11, color: "var(--text)" }}>
            {result.package}
          </span>
          {result.installs && <span style={{ fontSize: 10, color: "var(--text-dim)", flexShrink: 0 }}>{result.installs}</span>}
          {result.url && (
            <a href={result.url} target="_blank" rel="noreferrer" title={result.url} style={{ color: "var(--text-muted)", display: "inline-flex", flexShrink: 0 }}>
              <ExternalLink size={13} aria-hidden="true" />
              <span className="sr-only">Open {result.package}</span>
            </a>
          )}
          <button
            type="button"
            disabled={installing !== null}
            onClick={() => void install(result.package)}
            style={{
              padding: "3px 10px", borderRadius: 6, fontSize: 11, border: "none",
              background: installing === result.package ? "var(--bg-selected)" : "var(--accent)",
              color: installing === result.package ? "var(--text)" : "#fff", cursor: "pointer", flexShrink: 0,
            }}
          >
            {installing === result.package ? "Installing…" : "Install"}
          </button>
        </div>
      ))}
    </div>

    {/* Installed (annotated) skills */}
    <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginTop: 12, marginBottom: 6 }}>
      Installed <span style={{ fontWeight: 400, color: "var(--text-dim)", marginLeft: 4 }}>{installed.length}</span>
    </div>
    {loading && <div style={{ fontSize: 11, color: "var(--text-dim)" }}>Loading…</div>}
    {!loading && installed.length === 0 && (
      <div style={{ fontSize: 11, color: "var(--text-dim)" }}>No installed skills yet — search the marketplace above.</div>
    )}
    {!loading && installed.length > 0 && (
      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        {installed.map((skill) => {
          const install = skill.install!;
          const key = updateKey(install);
          const updateState = updates[key];
          return (
            <div key={`${skill.relPath}:${skill.skillId}`} style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 8, display: "flex", flexDirection: "column", gap: 6 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 6, minWidth: 0 }}>
                    <strong style={{ fontSize: 12, color: "var(--text)" }}>{skill.name}</strong>
                    <span className="skill-row-scope" style={{ fontSize: 9, padding: "1px 6px", borderRadius: 8, border: "1px solid var(--border)", color: "var(--text-muted)", flexShrink: 0 }}>
                      {install.scope}
                    </span>
                    {updateState?.state === "update-available" && (
                      <span style={{ fontSize: 9, padding: "1px 6px", borderRadius: 8, background: "#FFFBEB", border: "1px solid #FDE68A", color: "#92400E", flexShrink: 0 }}>
                        Update available
                      </span>
                    )}
                  </div>
                  {skill.description && (
                    <div style={{ fontSize: 10, color: "var(--text-muted)", marginTop: 2, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 420 }}>
                      {skill.description}
                    </div>
                  )}
                  <div style={{ fontSize: 10, color: "var(--text-dim)", marginTop: 2, display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center" }}>
                    <span style={{ fontFamily: "var(--font-mono, monospace)" }}>{install.package}</span>
                    {install.versionHash && <span style={{ fontFamily: "var(--font-mono, monospace)" }}>v{install.versionHash.slice(0, 8)}</span>}
                    {install.skillsShUrl && (
                      <a href={install.skillsShUrl} target="_blank" rel="noreferrer" style={{ display: "inline-flex", alignItems: "center", gap: 3 }}>
                        skills.sh <ExternalLink size={10} aria-hidden="true" />
                      </a>
                    )}
                  </div>
                </div>
                <div style={{ display: "flex", gap: 6, alignItems: "center", flexShrink: 0 }}>
                  <button
                    type="button"
                    title="Allow or block model invocation for this skill"
                    onClick={() => void toggle(skill)}
                    disabled={toggling === skill.relPath}
                    style={{
                      fontSize: 10, padding: "2px 8px", borderRadius: 6, border: "1px solid var(--border)",
                      background: skill.disableModelInvocation ? "var(--bg-selected)" : "var(--bg)",
                      color: skill.disableModelInvocation ? "var(--text-dim)" : "var(--text)", cursor: "pointer",
                    }}
                  >
                    {skill.disableModelInvocation ? "Disabled" : "Enabled"}
                  </button>
                  <button type="button" title="Check for updates" onClick={() => void check(install)}
                    disabled={checking !== null} style={ghostButtonStyle}>
                    {checking === key ? <RefreshCw size={12} aria-hidden="true" className="delivery-spinner" /> : "Check"}
                  </button>
                  {updateState?.state === "update-available" && (
                    <button type="button" onClick={() => void update(install)} disabled={checking !== null}
                      style={{ ...ghostButtonStyle, color: "#92400E", borderColor: "#FDE68A" }}>
                      Update
                    </button>
                  )}
                  {updateState?.state === "up-to-date" && (
                    <span style={{ fontSize: 10, color: "var(--text-dim)" }}>Up to date</span>
                  )}
                  {updateState?.state === "error" && updateState.message && (
                    <span style={{ fontSize: 10, color: "#E11D48", maxWidth: 180, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={updateState.message}>
                      {updateState.message}
                    </span>
                  )}
                  {updateState?.state === "unsupported" && (
                    <span style={{ fontSize: 10, color: "var(--text-dim)" }} title={updateState.message ?? ""}>No updates</span>
                  )}
                  <button type="button" title="Remove skill" onClick={() => void remove(skill)} disabled={removing !== null}
                    style={{ ...ghostButtonStyle, color: "#E11D48" }}>
                    {removing === skill.relPath ? "Removing…" : <X size={12} aria-hidden="true" />}
                  </button>
                </div>
              </div>
            </div>
          );
        })}
      </div>
    )}

    {/* Local (non-installed) catalog skills */}
    {local.length > 0 && (
      <>
        <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginTop: 12, marginBottom: 6 }}>
          Catalog <span style={{ fontWeight: 400, color: "var(--text-dim)", marginLeft: 4 }}>{local.length}</span>
        </div>
        <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
          {local.map((skill) => (
            <div key={`${skill.relPath}:${skill.skillId}`} style={{ display: "flex", alignItems: "center", gap: 8, padding: "4px 8px", border: "1px solid var(--border)", borderRadius: 6 }}>
              <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontSize: 11, color: "var(--text)" }}>
                {skill.name}
              </span>
              <span style={{ fontSize: 9, padding: "1px 6px", borderRadius: 8, border: "1px solid var(--border)", color: "var(--text-muted)", flexShrink: 0 }}>
                {skill.sourceInfo.scope ?? "local"}
              </span>
              {skill.disableModelInvocation && <span style={{ fontSize: 9, color: "var(--text-dim)", flexShrink: 0 }}>invocation disabled</span>}
            </div>
          ))}
        </div>
      </>
    )}

    {/* Diagnostics */}
    {diagnostics.length > 0 && (
      <div style={{ marginTop: 12 }}>
        <div style={{ fontSize: 11, fontWeight: 700, color: "var(--text-muted)", marginBottom: 4 }}>Catalog diagnostics</div>
        <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
          {diagnostics.map((diag, index) => (
            <div key={index} style={{ fontSize: 10, color: diag.level === "skip" ? "#92400E" : "var(--text-dim)", fontFamily: "var(--font-mono, monospace)" }}>
              [{diag.level}] {diag.relPath ? `${diag.relPath}: ` : ""}{diag.message}
            </div>
          ))}
        </div>
      </div>
    )}
  </section>;
}

const inputStyle: React.CSSProperties = {
  padding: "6px 8px", borderRadius: 6, border: "1px solid var(--border)",
  background: "var(--bg)", color: "var(--text)", fontSize: 12, flex: 1, minWidth: 180,
};

const ghostButtonStyle: React.CSSProperties = {
  padding: "3px 8px", borderRadius: 6, fontSize: 10,
  border: "1px solid var(--border)", background: "var(--bg)",
  color: "var(--text-muted)", cursor: "pointer", display: "inline-flex", alignItems: "center", gap: 4,
};
