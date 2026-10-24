"use client";

import { ExternalLink, RefreshCw, Search, X } from "lucide-react";
import type { AnnotatedSkill, SkillInstallInfo, SkillRoot, SkillSearchResult, SkillUpdateResult } from "@/components/settings/types";
import type { useSkillRoots } from "@/hooks/useSkillRoots";
import styles from "./SkillPanels.module.css";

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
      <div className={styles.sectionHeader}>
        Skills roots <span className={styles.sectionCount}>{roots.length} additional</span>
      </div>
      <div className={styles.note}>
        Resolution paths are on the Worker host. Remote clients (Tauri) read the same configured roots — no per-client setup.
      </div>
      <div className={styles.stack}>
        {roots.length === 0 && !loading && (
          <div className={styles.empty}>
            No additional roots. Ennote reads <code>$ENNOTE_HOME/skills</code> on the Worker host by default; add pi, Claude Code, Codex, or Cursor skill directories here.
          </div>
        )}
        {roots.map((root) => (
          <div key={root.id} className={styles.rootRow}>
            <div className={styles.rootIdentity}>
              <div className={styles.rootTitleRow}>
                <span className={styles.rootName}>{root.name}</span>
                <span className={styles.badge}>{root.agentKind}</span>
                <span className={styles.rootPriority}>pri {root.priority}</span>
              </div>
              <div className={styles.rootPath}>{root.path}</div>
            </div>
            <button
              type="button"
              onClick={() => void toggleRoot(root)}
              disabled={rootBusy !== null}
              className={styles.toggle}
              data-enabled={root.enabled || undefined}
            >
              {root.enabled ? "Enabled" : "Disabled"}
            </button>
            <button type="button" onClick={() => void removeRoot(root)} disabled={rootBusy !== null}
              title={`Stop reading skills from ${root.path}`}
              className={`${styles.ghostButton} ${styles.danger}`}>
              <X size={12} aria-hidden="true" />
            </button>
          </div>
        ))}
        {!showRootForm ? (
          <button type="button" onClick={() => setShowRootForm(true)} className={styles.ghostButton}>
            + Add skills root
          </button>
        ) : (
          <div className={styles.rootForm}>
            <div className={styles.inputRow}>
              <select
                value={rootPreset}
                onChange={(event) => setRootPreset(event.target.value)}
                aria-label="Ecosystem preset"
                className={`${styles.input} ${styles.inputPreset}`}
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
                className={`${styles.input} ${styles.inputName}`}
              />
              <input
                value={rootPath}
                onChange={(event) => setRootPath(event.target.value)}
                placeholder={rootPreset === "generic" ? "/absolute/path/to/skills" : "Optional explicit path (else preset)"}
                aria-label="Root path"
                className={styles.input}
              />
            </div>
            <div className={styles.actionRow}>
              <button type="button" onClick={() => void addRoot()} disabled={rootBusy !== null}
                className={styles.primaryButton}>
                {rootBusy === "add" ? "Adding…" : "Add root"}
              </button>
              <button type="button" onClick={() => setShowRootForm(false)} className={styles.ghostButton}>
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
    <div className={`skills-market-panel ${styles.market}`}>
      <div className={styles.marketTitle}>Marketplace</div>
      <div className={styles.searchRow}>
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => { if (event.key === "Enter") void onSearch(); }}
          placeholder="Search skills.sh…"
          aria-label="Search skills.sh"
          className={styles.input}
        />
        <select
          value={installScope}
          onChange={(event) => setInstallScope(event.target.value as "global" | "project")}
          aria-label="Install scope"
          className={`${styles.input} ${styles.inputScope}`}
        >
          <option value="global">Global</option>
          <option value="project" disabled={!projectId || !projectTrusted}>Project{!projectTrusted && projectId ? " (needs trust)" : ""}</option>
        </select>
        <button type="button" onClick={() => void onSearch()} disabled={searching || !query.trim()}
          className={styles.ghostButton}>
          <Search size={13} aria-hidden="true" /> {searching ? "Searching…" : "Search"}
        </button>
      </div>
      {searched && results.length === 0 && !searching && (
        <div className={styles.empty}>No skills match &quot;{query.trim()}&quot;.</div>
      )}
      {results.map((result) => (
        <div key={result.package} className={styles.resultRow}>
          <span className={styles.resultPackage}>
            {result.package}
          </span>
          {result.installs && <span className={styles.resultInstalls}>{result.installs}</span>}
          {result.url && (
            <a href={result.url} target="_blank" rel="noreferrer" title={result.url} className={`${styles.link} ${styles.linkIcon}`}>
              <ExternalLink size={13} aria-hidden="true" />
              <span className="sr-only">Open {result.package}</span>
            </a>
          )}
          <button
            type="button"
            disabled={installing !== null}
            onClick={() => void onInstall(result.package)}
            className={styles.installButton}
            data-busy={installing === result.package || undefined}
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
      <div className={styles.sectionHeader}>
        Installed <span className={styles.sectionCount}>{installed.length}</span>
      </div>
      {loading && <div className={styles.empty}>Loading…</div>}
      {!loading && installed.length === 0 && (
        <div className={styles.empty}>No installed skills yet — search the marketplace above.</div>
      )}
      {!loading && installed.length > 0 && (
        <div className={styles.stack}>
          {installed.map((skill) => {
            const install = skill.install!;
            const key = updateKey(install);
            const updateState = updates[key];
            return (
              <div key={`${skill.relPath}:${skill.skillId}`} className={styles.installedCard}>
                <div className={styles.installedHead}>
                  <div className={styles.installedIdentity}>
                    <div className={styles.installedTitleRow}>
                      <strong className={styles.installedName}>{skill.name}</strong>
                      <span className={`skill-row-scope ${styles.badge}`}>{install.scope}</span>
                      {updateState?.state === "update-available" && (
                        <span className={styles.updateBadge}>Update available</span>
                      )}
                    </div>
                    {skill.description && (
                      <div className={styles.installedDescription}>
                        {skill.description}
                      </div>
                    )}
                    <div className={styles.installedMeta}>
                      <span className={styles.mono}>{install.package}</span>
                      {install.versionHash && <span className={styles.mono}>v{install.versionHash.slice(0, 8)}</span>}
                      {install.skillsShUrl && (
                        <a href={install.skillsShUrl} target="_blank" rel="noreferrer" className={styles.link}>
                          skills.sh <ExternalLink size={10} aria-hidden="true" />
                        </a>
                      )}
                    </div>
                  </div>
                  <div className={styles.installedActions}>
                    <button
                      type="button"
                      title="Allow or block model invocation for this skill"
                      onClick={() => void onToggle(skill)}
                      disabled={toggling === skill.relPath}
                      className={styles.toggle}
                      data-disabled={skill.disableModelInvocation || undefined}
                    >
                      {skill.disableModelInvocation ? "Disabled" : "Enabled"}
                    </button>
                    <button type="button" title="Check for updates" onClick={() => void onCheck(install)}
                      disabled={checking !== null} className={styles.ghostButton}>
                      {checking === key ? <RefreshCw size={12} aria-hidden="true" className="delivery-spinner" /> : "Check"}
                    </button>
                    {updateState?.state === "update-available" && (
                      <button type="button" onClick={() => void onUpdate(install)} disabled={checking !== null}
                        className={`${styles.ghostButton} ${styles.warning}`}>
                        Update
                      </button>
                    )}
                    {updateState?.state === "up-to-date" && (
                      <span className={styles.statusText}>Up to date</span>
                    )}
                    {updateState?.state === "error" && updateState.message && (
                      <span className={styles.statusError} title={updateState.message}>
                        {updateState.message}
                      </span>
                    )}
                    {updateState?.state === "unsupported" && (
                      <span className={styles.statusText} title={updateState.message ?? ""}>No updates</span>
                    )}
                    <button type="button" title="Remove skill" onClick={() => void onRemove(skill)} disabled={removing !== null}
                      className={`${styles.ghostButton} ${styles.danger}`}>
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
      <div className={styles.sectionHeader}>
        Catalog <span className={styles.sectionCount}>{local.length}</span>
      </div>
      <div className={styles.stack}>
        {local.map((skill) => (
          <div key={`${skill.relPath}:${skill.skillId}`} className={styles.catalogRow}>
            <span className={styles.catalogName}>
              {skill.name}
            </span>
            <span className={styles.badge}>
              {skill.sourceInfo.scope ?? "local"}
            </span>
            {skill.disableModelInvocation && <span className={styles.statusText}>invocation disabled</span>}
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
    <div className={styles.diagnostics}>
      <div className={styles.diagTitle}>Catalog diagnostics</div>
      <div className={styles.diagList}>
        {diagnostics.map((diag, index) => (
          <div key={index} className={styles.diagRow} data-level={diag.level === "skip" ? "skip" : undefined}>
            [{diag.level}] {diag.relPath ? `${diag.relPath}: ` : ""}{diag.message}
          </div>
        ))}
      </div>
    </div>
  );
}
