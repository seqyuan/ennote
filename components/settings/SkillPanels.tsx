"use client";

import { ExternalLink, RefreshCw, Search, X } from "lucide-react";
import { SettingsEmpty, SettingsLoading, SettingsSection } from "@/components/settings/SettingsPrimitives";
import { useT } from "@/components/LocaleProvider";
import type { AnnotatedSkill, SkillInstallInfo, SkillRoot, SkillSearchResult, SkillUpdateResult } from "@/components/settings/types";
import type { useSkillRoots } from "@/hooks/useSkillRoots";
import styles from "./SkillPanels.module.css";

export function SkillRootsPanel(props: {
  roots: SkillRoot[];
  loading: boolean;
  control: ReturnType<typeof useSkillRoots>;
}) {
  const t = useT();
  const { roots, loading, control } = props;
  const {
    showRootForm, setShowRootForm, rootName, setRootName, rootPreset, setRootPreset,
    rootPath, setRootPath, rootBusy, addRoot, toggleRoot, removeRoot,
  } = control;

  return (
    <SettingsSection
      title={<>{t("settings.skills.rootsTitle")} <span className={styles.sectionCount}>{t("settings.skills.rootsAdditional", { count: roots.length })}</span></>}
      description={t("settings.skills.rootsDescription")}
    >
      <div className={styles.stack}>
        {roots.length === 0 && !loading && (
          <SettingsEmpty>
            {t("settings.skills.rootsEmpty", { path: "$ENNOTE_HOME/skills" })}
          </SettingsEmpty>
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
              className={root.enabled ? "settings-btn primary" : "settings-btn"}
            >
              {root.enabled ? t("settings.skills.enabled") : t("settings.skills.disabled")}
            </button>
            <button type="button" onClick={() => void removeRoot(root)} disabled={rootBusy !== null}
              title={t("settings.skills.stopReadingTitle", { path: root.path })}
              className="settings-btn danger">
              <X size={12} aria-hidden="true" />
            </button>
          </div>
        ))}
        {!showRootForm ? (
          <button type="button" onClick={() => setShowRootForm(true)} className="settings-btn">
            {t("settings.skills.addRootLink")}
          </button>
        ) : (
          <div className={styles.rootForm}>
            <div className={styles.inputRow}>
              <select
                value={rootPreset}
                onChange={(event) => setRootPreset(event.target.value)}
                aria-label={t("settings.skills.ecosystemPreset")}
                className={`${styles.input} ${styles.inputPreset}`}
                disabled={Boolean(rootPath.trim())}
              >
                <option value="pi">pi</option>
                <option value="claude">Claude Code</option>
                <option value="codex">Codex</option>
                <option value="cursor">Cursor</option>
                <option value="generic">{t("settings.skills.preset.custom")}</option>
              </select>
              <input
                value={rootName}
                onChange={(event) => setRootName(event.target.value)}
                placeholder={t("settings.skills.namePlaceholder")}
                aria-label={t("settings.skills.rootName")}
                className={`${styles.input} ${styles.inputName}`}
              />
              <input
                value={rootPath}
                onChange={(event) => setRootPath(event.target.value)}
                placeholder={rootPreset === "generic" ? t("settings.skills.pathPlaceholder") : t("settings.skills.explicitPathPlaceholder")}
                aria-label={t("settings.skills.rootPath")}
                className={styles.input}
              />
            </div>
            <div className={styles.actionRow}>
              <button type="button" onClick={() => void addRoot()} disabled={rootBusy !== null}
                className="settings-btn primary">
                {rootBusy === "add" ? t("settings.skills.adding") : t("settings.skills.addRoot")}
              </button>
              <button type="button" onClick={() => setShowRootForm(false)} className="settings-btn">
                {t("settings.skills.cancel")}
              </button>
            </div>
          </div>
        )}
      </div>
    </SettingsSection>
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
  const t = useT();
  const {
    projectId, projectTrusted, query, setQuery, results, searching, searched,
    installScope, setInstallScope, installing, onSearch, onInstall,
  } = props;

  return (
    <SettingsSection title={t("settings.skills.marketplace")}>
      <div className={styles.searchRow}>
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => { if (event.key === "Enter") void onSearch(); }}
          placeholder={t("settings.skills.searchPlaceholder")}
          aria-label={t("settings.skills.searchAria")}
          className={styles.input}
        />
        <select
          value={installScope}
          onChange={(event) => setInstallScope(event.target.value as "global" | "project")}
          aria-label={t("settings.skills.installScope")}
          className={`${styles.input} ${styles.inputScope}`}
        >
          <option value="global">{t("settings.skills.scope.global")}</option>
          <option value="project" disabled={!projectId || !projectTrusted}>{t("settings.skills.scope.project")}{!projectTrusted && projectId ? t("settings.skills.scope.needsTrust") : ""}</option>
        </select>
        <button type="button" onClick={() => void onSearch()} disabled={searching || !query.trim()}
          className="settings-btn">
          <Search size={13} aria-hidden="true" /> {searching ? t("settings.skills.searching") : t("settings.skills.search")}
        </button>
      </div>
      {searched && results.length === 0 && !searching && (
        <SettingsEmpty>{t("settings.skills.noMatch", { query: query.trim() })}</SettingsEmpty>
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
              <span className="sr-only">{t("settings.skills.open", { package: result.package })}</span>
            </a>
          )}
          <button
            type="button"
            disabled={installing !== null}
            onClick={() => void onInstall(result.package)}
            className="settings-btn primary"
          >
            {installing === result.package ? t("settings.skills.installing") : t("settings.skills.install")}
          </button>
        </div>
      ))}
    </SettingsSection>
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
  const t = useT();
  const { installed, loading, updateKey, updates, checking, toggling, removing, onCheck, onUpdate, onToggle, onRemove } = props;

  return (
    <SettingsSection title={<>{t("settings.skills.installed")} <span className={styles.sectionCount}>{installed.length}</span></>}>
      {loading && <SettingsLoading label="Loading…" />}
      {!loading && installed.length === 0 && (
        <SettingsEmpty>{t("settings.skills.installedEmpty")}</SettingsEmpty>
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
                        <span className={styles.updateBadge}>{t("settings.skills.updateAvailable")}</span>
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
                      title={t("settings.skills.invocationTitle")}
                      onClick={() => void onToggle(skill)}
                      disabled={toggling === skill.relPath}
                      className={skill.disableModelInvocation ? "settings-btn" : "settings-btn primary"}
                    >
                      {skill.disableModelInvocation ? t("settings.skills.disabled") : t("settings.skills.enabled")}
                    </button>
                    <button type="button" title={t("settings.skills.checkTitle")} onClick={() => void onCheck(install)}
                      disabled={checking !== null} className="settings-btn">
                      {checking === key ? <RefreshCw size={12} aria-hidden="true" className="delivery-spinner" /> : t("settings.skills.check")}
                    </button>
                    {updateState?.state === "update-available" && (
                      <button type="button" onClick={() => void onUpdate(install)} disabled={checking !== null}
                        className="settings-btn warning">
                        {t("settings.skills.update")}
                      </button>
                    )}
                    {updateState?.state === "up-to-date" && (
                      <span className={styles.statusText}>{t("settings.skills.upToDate")}</span>
                    )}
                    {updateState?.state === "error" && updateState.message && (
                      <span className={styles.statusError} title={updateState.message}>
                        {updateState.message}
                      </span>
                    )}
                    {updateState?.state === "unsupported" && (
                      <span className={styles.statusText} title={updateState.message ?? ""}>{t("settings.skills.noUpdates")}</span>
                    )}
                    <button type="button" title={t("settings.skills.removeTitle")} onClick={() => void onRemove(skill)} disabled={removing !== null}
                      className="settings-btn danger">
                      {removing === skill.relPath ? t("settings.skills.removing") : <X size={12} aria-hidden="true" />}
                    </button>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </SettingsSection>
  );
}

export function SkillCatalogList({ local }: { local: AnnotatedSkill[] }) {
  const t = useT();
  if (local.length === 0) return null;
  return (
    <SettingsSection title={<>{t("settings.skills.catalog")} <span className={styles.sectionCount}>{local.length}</span></>}>
      <div className={styles.stack}>
        {local.map((skill) => (
          <div key={`${skill.relPath}:${skill.skillId}`} className={styles.catalogRow}>
            <span className={styles.catalogName}>
              {skill.name}
            </span>
            <span className={styles.badge}>
              {skill.sourceInfo.scope ?? t("settings.skills.catalogLocal")}
            </span>
            {skill.disableModelInvocation && <span className={styles.statusText}>{t("settings.skills.invocationDisabled")}</span>}
          </div>
        ))}
      </div>
    </SettingsSection>
  );
}

export function SkillDiagnostics({ diagnostics }: {
  diagnostics: { level?: string; message?: string; relPath?: string; source?: string }[];
}) {
  const t = useT();
  if (diagnostics.length === 0) return null;
  return (
    <div className={styles.diagnostics}>
      <div className={styles.diagTitle}>{t("settings.skills.diagnostics")}</div>
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
