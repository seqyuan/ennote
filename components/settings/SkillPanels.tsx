"use client";

import { ExternalLink, RefreshCw, Search, X } from "lucide-react";
import type { AnnotatedSkill, SkillInstallInfo, SkillRoot, SkillSearchResult, SkillUpdateResult } from "@/components/settings/types";
import type { useSkillRoots } from "@/hooks/useSkillRoots";

const inputStyle: React.CSSProperties = {
  padding: "6px 8px", borderRadius: 6, border: "1px solid var(--border)",
  background: "var(--bg)", color: "var(--text)", fontSize: 12, flex: 1, minWidth: 180,
};

const ghostButtonStyle: React.CSSProperties = {
  padding: "3px 8px", borderRadius: 6, fontSize: 10,
  border: "1px solid var(--border)", background: "var(--bg)",
  color: "var(--text-muted)", cursor: "pointer", display: "inline-flex", alignItems: "center", gap: 4,
};

export function SkillRootsPanel(props: {
  roots: SkillRoot[];
  loading: boolean;
  control: ReturnType<typeof useSkillRoots>;
}) {
  const { roots, loading, control } = props;
  const {
    showRootForm, setShowRootForm, rootName, setRootName, rootPreset, setRootPreset,
    rootPath, setRootPath, rootBusy, addRoot, toggleRoot, removeRoot,
  } = control;

  return (
    <>
      <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginTop: 12, marginBottom: 6 }}>
        Skills roots <span style={{ fontWeight: 400, color: "var(--text-dim)", marginLeft: 4 }}>{roots.length} additional</span>
      </div>
      <div style={{ fontSize: 10, color: "var(--text-dim)", marginBottom: 6 }}>
        Resolution paths are on the Worker host. Remote clients (Tauri) read the same configured roots — no per-client setup.
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        {roots.length === 0 && !loading && (
          <div style={{ fontSize: 11, color: "var(--text-dim)" }}>
            No additional roots. Ennote reads <code>$ENNOTE_HOME/skills</code> on the Worker host by default; add pi, Claude Code, Codex, or Cursor skill directories here.
          </div>
        )}
        {roots.map((root) => (
          <div key={root.id} style={{ display: "flex", alignItems: "center", gap: 8, padding: "6px 8px", border: "1px solid var(--border)", borderRadius: 6 }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                <span style={{ fontSize: 12, fontWeight: 600, color: "var(--text)" }}>{root.name}</span>
                <span style={{ fontSize: 9, padding: "1px 6px", borderRadius: 8, border: "1px solid var(--border)", color: "var(--text-muted)", flexShrink: 0 }}>
                  {root.agentKind}
                </span>
                <span style={{ fontSize: 9, color: "var(--text-dim)", flexShrink: 0 }}>pri {root.priority}</span>
              </div>
              <div style={{ fontSize: 10, color: "var(--text-dim)", fontFamily: "var(--font-mono, monospace)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 380 }}>
                {root.path}
              </div>
            </div>
            <button
              type="button"
              onClick={() => void toggleRoot(root)}
              disabled={rootBusy !== null}
              style={{
                fontSize: 10, padding: "2px 8px", borderRadius: 6, border: "1px solid var(--border)",
                background: root.enabled ? "var(--accent)" : "var(--bg)",
                color: root.enabled ? "#fff" : "var(--text)", cursor: "pointer", flexShrink: 0,
              }}
            >
              {root.enabled ? "Enabled" : "Disabled"}
            </button>
            <button type="button" onClick={() => void removeRoot(root)} disabled={rootBusy !== null}
              title={`Stop reading skills from ${root.path}`}
              style={{ ...ghostButtonStyle, color: "#E11D48" }}>
              <X size={12} aria-hidden="true" />
            </button>
          </div>
        ))}
        {!showRootForm ? (
          <button type="button" onClick={() => setShowRootForm(true)} style={ghostButtonStyle}>
            + Add skills root
          </button>
        ) : (
          <div style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 10, display: "flex", flexDirection: "column", gap: 8 }}>
            <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
              <select
                value={rootPreset}
                onChange={(event) => setRootPreset(event.target.value)}
                aria-label="Ecosystem preset"
                style={{ ...inputStyle, width: 130 }}
                disabled={Boolean(rootPath.trim())}
              >
                <option value="pi">pi</option>
                <option value="claude">Claude Code</option>
                <option value="codex">Codex</option>
                <option value="cursor">Cursor</option>
                <option value="generic">Custom path</option>
              </select>
              <input
                value={rootName}
                onChange={(event) => setRootName(event.target.value)}
                placeholder="Name (defaults to preset)"
                aria-label="Root name"
                style={{ ...inputStyle, minWidth: 140, flex: 0 }}
              />
              <input
                value={rootPath}
                onChange={(event) => setRootPath(event.target.value)}
                placeholder={rootPreset === "generic" ? "/absolute/path/to/skills" : "Optional explicit path (else preset)"}
                aria-label="Root path"
                style={inputStyle}
              />
            </div>
            <div style={{ display: "flex", gap: 6 }}>
              <button type="button" onClick={() => void addRoot()} disabled={rootBusy !== null}
                style={{ padding: "4px 12px", borderRadius: 6, border: "none", background: "var(--accent)", color: "#fff", fontSize: 11, cursor: "pointer" }}>
                {rootBusy === "add" ? "Adding…" : "Add root"}
              </button>
              <button type="button" onClick={() => setShowRootForm(false)} style={ghostButtonStyle}>
                Cancel
              </button>
            </div>
          </div>
        )}
      </div>
    </>
  );
}

export function SkillMarketplace(props: {
  projectId: string | null;
  projectTrusted: boolean;
  query: string;
  setQuery: (value: string) => void;
  results: SkillSearchResult[];
  searching: boolean;
  searched: boolean;
  installScope: "global" | "project";
  setInstallScope: (scope: "global" | "project") => void;
  installing: string | null;
  onSearch: () => Promise<void>;
  onInstall: (pkg: string) => Promise<void>;
}) {
  const {
    projectId, projectTrusted, query, setQuery, results, searching, searched,
    installScope, setInstallScope, installing, onSearch, onInstall,
  } = props;

  return (
    <div className="skills-market-panel" style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 10, display: "flex", flexDirection: "column", gap: 8 }}>
      <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)" }}>Marketplace</div>
      <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => { if (event.key === "Enter") void onSearch(); }}
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
        <button type="button" onClick={() => void onSearch()} disabled={searching || !query.trim()}
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
            onClick={() => void onInstall(result.package)}
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
  );
}

export function SkillInstalledList(props: {
  installed: AnnotatedSkill[];
  loading: boolean;
  updateKey: (install: { package: string; scope: string }) => string;
  updates: Record<string, SkillUpdateResult>;
  checking: string | null;
  toggling: string | null;
  removing: string | null;
  onCheck: (install: SkillInstallInfo) => Promise<void>;
  onUpdate: (install: SkillInstallInfo) => Promise<void>;
  onToggle: (skill: AnnotatedSkill) => Promise<void>;
  onRemove: (skill: AnnotatedSkill) => Promise<void>;
}) {
  const { installed, loading, updateKey, updates, checking, toggling, removing, onCheck, onUpdate, onToggle, onRemove } = props;

  return (
    <>
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
                      onClick={() => void onToggle(skill)}
                      disabled={toggling === skill.relPath}
                      style={{
                        fontSize: 10, padding: "2px 8px", borderRadius: 6, border: "1px solid var(--border)",
                        background: skill.disableModelInvocation ? "var(--bg-selected)" : "var(--bg)",
                        color: skill.disableModelInvocation ? "var(--text-dim)" : "var(--text)", cursor: "pointer",
                      }}
                    >
                      {skill.disableModelInvocation ? "Disabled" : "Enabled"}
                    </button>
                    <button type="button" title="Check for updates" onClick={() => void onCheck(install)}
                      disabled={checking !== null} style={ghostButtonStyle}>
                      {checking === key ? <RefreshCw size={12} aria-hidden="true" className="delivery-spinner" /> : "Check"}
                    </button>
                    {updateState?.state === "update-available" && (
                      <button type="button" onClick={() => void onUpdate(install)} disabled={checking !== null}
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
                    <button type="button" title="Remove skill" onClick={() => void onRemove(skill)} disabled={removing !== null}
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
    </>
  );
}

export function SkillCatalogList({ local }: { local: AnnotatedSkill[] }) {
  if (local.length === 0) return null;
  return (
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
  );
}

export function SkillDiagnostics({ diagnostics }: {
  diagnostics: { level?: string; message?: string; relPath?: string; source?: string }[];
}) {
  if (diagnostics.length === 0) return null;
  return (
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
  );
}
