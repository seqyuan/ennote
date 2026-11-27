"use client";

import { Archive, Loader2 } from "lucide-react";
import type { MCPCatalogEntry, MCPProjectBinding } from "@/components/settings/types";
import { SettingsSection } from "@/components/settings/SettingsPrimitives";
import { confirmAction } from "@/components/ui/confirm";
import { useT } from "@/components/LocaleProvider";
import type { McpActionOutcome, useMcpSettings } from "@/hooks/useMcpSettings";
import styles from "./McpPanels.module.css";

const TRANSPORT_LABELS: Record<string, string> = {
  stdio: "stdio",
  streamable_http: "Streamable HTTP",
  legacy_sse: "HTTP + SSE",
};

export function McpCreateForm({ mcp }: { mcp: ReturnType<typeof useMcpSettings> }) {
  const t = useT();
  const {
    showCreate, transport, setTransport, displayName, setDisplayName,
    slug, setSlug, executable, setExecutable, argv, setArgv, endpoint, setEndpoint, createProfile,
  } = mcp;

  return (
    <>
      {showCreate && (
        <div className={styles.form}>
          <div className={styles.formGrid}>
            <label className={styles.field}>{t("settings.mcp.displayName")}
              <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} className={styles.input} placeholder="PubMed" />
            </label>
            <label className={styles.field}>{t("settings.mcp.slug")}
              <input value={slug} onChange={(e) => setSlug(e.target.value)} className={styles.input} placeholder="pubmed" />
            </label>
          </div>
          <div>
            <div className={styles.transportRow}>
              {(["stdio", "streamable_http", "legacy_sse"] as const).map((kind) => (
                <button
                  key={kind}
                  type="button"
                  onClick={() => setTransport(kind)}
                  className={styles.transportButton}
                  data-active={transport === kind || undefined}
                >
                  {TRANSPORT_LABELS[kind]}
                </button>
              ))}
            </div>
            {transport === "stdio" ? (
              <>
                <label className={styles.field}>{t("settings.mcp.executable")}
                  <input value={executable} onChange={(e) => setExecutable(e.target.value)} className={styles.input} placeholder="/usr/bin/python3" />
                </label>
                <label className={styles.field}>{t("settings.mcp.arguments")}
                  <input value={argv} onChange={(e) => setArgv(e.target.value)} className={styles.input} placeholder="/path/to/server.py" />
                </label>
              </>
            ) : (
              <label className={styles.field}>{t("settings.mcp.endpoint")}
                <input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} className={styles.input} placeholder="https://example.com/mcp" />
              </label>
            )}
          </div>
          <button
            type="button"
            onClick={createProfile}
            disabled={!displayName.trim() || !slug.trim() || (transport === "stdio" ? !executable.trim() : !endpoint.trim())}
            className="settings-btn primary"
          >
            {t("settings.mcp.createProfile")}
          </button>
        </div>
      )}
    </>
  );
}

export function McpBindingCard({ mcp, binding }: { mcp: ReturnType<typeof useMcpSettings>; binding: MCPProjectBinding }) {
  const t = useT();
  const {
    profiles, catalogs, toolSearch, setToolSearch, showCreds, setShowCreds, credEnv, setCredEnv, credRef, setCredRef,
    versionFor, updateBinding, toggleTool, selectAllTools, addCredential, removeCredential, testBinding, refreshCatalog, removeBinding,
    bindingActions, actionResults,
  } = mcp;
  const catalog = catalogs[binding.id!] ?? [];
  const selected = binding.selectedRemoteToolNames ?? [];
  const version = versionFor(binding);
  const commandSummary = version
    ? version.transport === "stdio"
      ? `${version.executable ?? ""} ${(version.argv ?? []).join(" ")}`.trim()
      : version.endpoint ?? ""
    : "";
  // A binding carries no display name (contract), so the profile behind its
  // frozen version names the card; the connection target is the next best
  // identity, and the transport label is the last resort.
  const profile = version ? profiles.find((entry) => entry.id === version.profileId) : undefined;
  const title = profile?.displayName || commandSummary || (version ? TRANSPORT_LABELS[version.transport] ?? version.transport : t("settings.mcp.serverFallback"));
  const runningAction = bindingActions[binding.id!];
  const outcome: McpActionOutcome | undefined = actionResults[binding.id!];

  return (
    <div key={binding.id} className={styles.card}>
      <div className={styles.cardHead}>
        <div className={styles.cardIdentity}>
          <div className={styles.cardTitle}>
            {title}
            <span className={styles.cardMeta}>
              {version && (
                <span className={styles.transportBadge}>{TRANSPORT_LABELS[version.transport] ?? version.transport}</span>
              )}
              rev {binding.revision ?? 0} · {selected.length} tools selected
              {outcome && (
                <span className={styles.resultBadge} data-ok={outcome.ok ? "true" : "false"} title={outcome.message}>
                  {outcome.action === "test" ? t("settings.mcp.badge.test") : t("settings.mcp.badge.refresh")} {outcome.ok ? t("settings.mcp.badge.ok") : t("settings.mcp.badge.failed")}
                </span>
              )}
            </span>
          </div>
          {commandSummary && (
            <div className={styles.commandSummary}>
              {commandSummary}
            </div>
          )}
        </div>
        <div className={styles.actions}>
          <button
            type="button"
            title={binding.required ? t("settings.mcp.requiredTitle") : t("settings.mcp.optionalTitle")}
            onClick={() => updateBinding(binding, { required: !binding.required })}
            className="settings-btn ghost"
            data-required={binding.required || undefined}
          >
            {binding.required ? t("settings.mcp.required") : t("settings.mcp.optional")}
          </button>
          <button
            type="button"
            title={t("settings.mcp.credentialsTitle")}
            onClick={() => setShowCreds((prev) => ({ ...prev, [binding.id!]: !prev[binding.id!] }))}
            className="settings-btn"
          >
            {t("settings.mcp.credentials")}
          </button>
          <button
            type="button"
            onClick={async () => {
              const next = !binding.desiredEnabled;
              if (next) {
                const summary = version
                  ? version.transport === "stdio"
                    ? t("settings.mcp.summary.command", { command: commandSummary })
                    : t("settings.mcp.summary.endpoint", { endpoint: version.endpoint ?? "" })
                  : "";
                if (!(await confirmAction({
                  title: t("settings.mcp.confirm.enableTitle"),
                  description: t("settings.mcp.confirm.enableDescription", { summary }),
                  confirmLabel: t("settings.mcp.confirm.enableLabel"),
                }))) return;
              }
              await updateBinding(binding, { desiredEnabled: next });
            }}
            className={binding.desiredEnabled ? "settings-btn primary" : "settings-btn"}
          >
            {binding.desiredEnabled ? t("settings.mcp.enabled") : t("settings.mcp.disabled")}
          </button>
          <button
            type="button"
            title={t("settings.mcp.testTitle")}
            onClick={() => testBinding(binding)}
            disabled={Boolean(runningAction)}
            className="settings-btn"
          >
            {runningAction === "test" ? <Loader2 size={12} className="spin" aria-hidden="true" /> : t("settings.mcp.test")}
          </button>
          <button
            type="button"
            title={t("settings.mcp.refreshTitle")}
            onClick={() => refreshCatalog(binding)}
            disabled={Boolean(runningAction)}
            className="settings-btn"
          >
            {runningAction === "refresh" ? <Loader2 size={12} className="spin" aria-hidden="true" /> : t("settings.mcp.refresh")}
          </button>
          <button type="button" title={t("settings.mcp.removeTitle")} onClick={() => removeBinding(binding)}
            className="settings-btn danger">
            {t("settings.mcp.remove")}
          </button>
        </div>
      </div>
      {binding.desiredEnabled && (
        <div>
          <div className={styles.toolsHeader}>
            <span className={styles.toolsHint}>{t("settings.mcp.toolsHint")}</span>
            {catalog.length > 0 && (
              <button type="button" onClick={() => selectAllTools(binding, catalog)} className="settings-btn">
                {t("settings.mcp.selectAll")}
              </button>
            )}
          </div>
          {catalog.length === 0 ? (
            <div className={styles.catalogEmpty}>
              {t("settings.mcp.noCatalog")}
            </div>
          ) : (
            <div>
              <input
                type="search"
                placeholder={t("settings.mcp.searchTools")}
                value={toolSearch[binding.id!] ?? ""}
                onChange={(e) => setToolSearch((prev) => ({ ...prev, [binding.id!]: e.target.value }))}
                className={`${styles.input} ${styles.toolSearch}`}
              />
              <div className={styles.catalogScroll}>
                <table className={styles.catalogTable}>
                  <tbody>
                    {catalog
                      .filter((entry: MCPCatalogEntry) => {
                        const q = (toolSearch[binding.id!] ?? "").toLowerCase();
                        if (!q) return true;
                        return (
                          entry.remoteName!.toLowerCase().includes(q) ||
                          (entry.description ?? "").toLowerCase().includes(q)
                        );
                      })
                      .map((entry: MCPCatalogEntry) => (
                        <tr key={entry.remoteName} className={styles.toolRow}>
                          <td className={styles.toolCheckCell}>
                            <input
                              type="checkbox"
                              className={styles.toolCheckbox}
                              aria-label={entry.remoteName}
                              checked={selected.includes(entry.remoteName!)}
                              onChange={() => toggleTool(binding, entry.remoteName!)}
                            />
                          </td>
                          <td className={styles.toolNameCell}>{entry.remoteName}</td>
                          <td className={styles.toolDescriptionCell}>
                            {entry.description || "—"}
                          </td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </div>
      )}

      {/* Credential references (refs only; values are never shown) */}
      {showCreds[binding.id!] && (
        <div className={styles.credentials}>
          <div className={styles.credentialNote}>
            {t("settings.mcp.credentialNote")}
          </div>
          {(Object.entries(binding.credentialRefs ?? {}) as [string, string][]).map(([env, ref]) => (
            <div key={env} className={styles.credentialRow}>
              <code className={styles.credentialEnv}>{env}</code>
              <code className={styles.credentialRef}>{ref}</code>
              <button type="button" onClick={() => removeCredential(binding, env)} className="settings-btn">
                {t("settings.mcp.remove")}
              </button>
            </div>
          ))}
          {Object.keys(binding.credentialRefs ?? {}).length === 0 && (
            <div className={styles.credentialNote}>
              {t("settings.mcp.noBindingCredentials")}
            </div>
          )}
          <div className={styles.credentialInputRow}>
            <input
              value={credEnv[binding.id!] ?? ""}
              onChange={(e) => setCredEnv((prev) => ({ ...prev, [binding.id!]: e.target.value }))}
              placeholder="ENV_NAME"
              className={`${styles.input} ${styles.credentialEnvInput}`}
            />
            <input
              value={credRef[binding.id!] ?? ""}
              onChange={(e) => setCredRef((prev) => ({ ...prev, [binding.id!]: e.target.value }))}
              placeholder="env:MY_REF / file:... / keyring:..."
              className={`${styles.input} ${styles.credentialRefInput}`}
            />
            <button
              type="button"
              onClick={() => addCredential(binding)}
              disabled={!(credEnv[binding.id!] ?? "").trim() || !(credRef[binding.id!] ?? "").trim()}
              className="settings-btn"
            >
              {t("settings.mcp.add")}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

export function McpCandidateSection({ mcp }: { mcp: ReturnType<typeof useMcpSettings> }) {
  const t = useT();
  const { profiles, candidates, bindings, versions, diffView, bindCandidate, openProfileDiff, archiveProfile } = mcp;

  if (candidates.length === 0 && profiles.length === 0) return null;
  return (
    <SettingsSection title={t("settings.mcp.discovered")}>
      <div className={styles.list}>
        {candidates.map((candidate) => (
          <div key={`${candidate.sourceKind}:${candidate.slug}`} className={styles.candidateGroup}>
          <div className={styles.candidateRow}>
            <div className={styles.candidateIdentity}>
              <span className={styles.candidateName}>{candidate.displayName ?? candidate.slug}</span>
              <span className={styles.candidateMeta}>
                {candidate.transport} · {candidate.sourceKind}
              </span>
              {candidate.sourceKind === "project_file" && (
                <div className={styles.candidateSource}>
                  {candidate.sourceLocator}
                </div>
              )}
            </div>
            <div className={styles.candidateActions}>
              {candidate.alreadyBound ? (
                <span className={styles.boundBadge}>
                  {t("settings.mcp.bound")}
                </span>
              ) : (
                <button type="button" onClick={() => bindCandidate(candidate)} className="settings-btn">
                  {t("settings.mcp.bind")}
                </button>
              )}
              {candidate.alreadyBound && candidate.updateAvailable && (
                <button
                  type="button"
                  title={t("settings.mcp.changedTitle")}
                  onClick={() => bindCandidate(candidate)}
                  className="settings-btn primary"
                >
                  {t("settings.mcp.updateAvailable")}
                </button>
              )}
              {candidate.alreadyBound && candidate.updateAvailable && (
                <button
                  type="button"
                  title={t("settings.mcp.diffTitle")}
                  onClick={() => openProfileDiff(candidate)}
                  className="settings-btn"
                >
                  {t("settings.mcp.viewDiff")}
                </button>
              )}
              {candidate.sourceKind === "managed" && !candidate.alreadyBound && (() => {
                const profile = profiles.find(entry => entry.slug === candidate.slug && entry.lifecycleStatus === "active");
                return profile ? (
                  <button type="button" title={t("settings.mcp.archiveTitle")}
                    aria-label={t("settings.mcp.archiveAria", { name: profile.displayName ?? profile.slug ?? "" })}
                    onClick={() => archiveProfile(profile)}
                    className="settings-btn danger">
                    <Archive size={14} aria-hidden="true" />
                  </button>
                ) : null;
              })()}
            </div>
          </div>
          {diffView && diffView.slug === candidate.slug && (
            <div className={styles.diffCard}>
              <div className={styles.diffTitle}>
                Read-only diff: bound version vs project file
                {diffView.digestTo && (
                  <span className={styles.diffDigest}>
                    new config {diffView.digestTo.slice(0, 8)}
                  </span>
                )}
              </div>
              {diffView.lines.some((line) => line.startsWith("+ ") || line.startsWith("- ")) ? (
                <div className={styles.diffBody}>
                  {diffView.lines.map((line, index) => (
                    <div
                      key={index}
                      className={styles.diffLine}
                      data-kind={line.startsWith("- ") ? "removed" : line.startsWith("+ ") ? "added" : undefined}
                    >
                      {line}
                    </div>
                  ))}
                </div>
              ) : (
                <div className={styles.diffUnchanged}>
                  Connection fields are unchanged — the project file changed argv, env, headers, or timeout (config digest {diffView.digestTo ? diffView.digestTo.slice(0, 8) : "changed"}).
                </div>
              )}
            </div>
          )}
          </div>
        ))}
        {/* Managed profiles already in the library but not listed as candidates */}
        {profiles.filter(profile => !candidates.some(candidate => candidate.slug === profile.slug)).map((profile) => {
          const profileVersionIds = new Set((versions[profile.id!] ?? []).map(version => version.id));
          const bound = bindings.some(binding => profileVersionIds.has(binding.profileVersionId));
          return (
            <div key={profile.id} className={styles.candidateRow}>
              <div className={styles.candidateIdentity}>
                <span className={styles.candidateName}>{profile.displayName}</span>
                <span className={styles.candidateMeta}>
                  {profile.slug} · v{profile.latestVersion ?? 0} · managed
                </span>
              </div>
              <div className={styles.candidateActions}>
                {bound ? (
                  <span className={styles.boundBadge}>
                    Bound
                  </span>
                ) : (
                  <button type="button" title={t("settings.mcp.archiveTitle")}
                    aria-label={t("settings.mcp.archiveAria", { name: profile.displayName ?? profile.slug ?? "" })}
                    onClick={() => archiveProfile(profile)}
                    className="settings-btn danger">
                    <Archive size={14} aria-hidden="true" />
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </SettingsSection>
  );
}
