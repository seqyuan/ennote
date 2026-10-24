"use client";

import { Archive } from "lucide-react";
import type { MCPCatalogEntry, MCPProjectBinding } from "@/components/settings/types";
import { confirmAction } from "@/components/ui/confirm";
import type { useMcpSettings } from "@/hooks/useMcpSettings";
import styles from "./McpPanels.module.css";

const TRANSPORT_LABELS: Record<string, string> = {
  stdio: "stdio",
  streamable_http: "Streamable HTTP",
  legacy_sse: "HTTP + SSE",
};

export function McpCreateForm({ mcp }: { mcp: ReturnType<typeof useMcpSettings> }) {
  const {
    showCreate, transport, setTransport, displayName, setDisplayName,
    slug, setSlug, executable, setExecutable, argv, setArgv, endpoint, setEndpoint, createProfile,
  } = mcp;

  return (
    <>
      {showCreate && (
        <div className={styles.form}>
          <div className={styles.formGrid}>
            <label className={styles.field}>Display name
              <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} className={styles.input} placeholder="PubMed" />
            </label>
            <label className={styles.field}>Slug (fixed once bound)
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
                <label className={styles.field}>Executable
                  <input value={executable} onChange={(e) => setExecutable(e.target.value)} className={styles.input} placeholder="/usr/bin/python3" />
                </label>
                <label className={styles.field}>Arguments (space-separated)
                  <input value={argv} onChange={(e) => setArgv(e.target.value)} className={styles.input} placeholder="/path/to/server.py" />
                </label>
              </>
            ) : (
              <label className={styles.field}>Endpoint URL
                <input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} className={styles.input} placeholder="https://example.com/mcp" />
              </label>
            )}
          </div>
          <button
            type="button"
            onClick={createProfile}
            disabled={!displayName.trim() || !slug.trim() || (transport === "stdio" ? !executable.trim() : !endpoint.trim())}
            className={styles.createButton}
          >
            Create profile
          </button>
        </div>
      )}
    </>
  );
}

export function McpBindingCard({ mcp, binding }: { mcp: ReturnType<typeof useMcpSettings>; binding: MCPProjectBinding }) {
  const {
    catalogs, toolSearch, setToolSearch, showCreds, setShowCreds, credEnv, setCredEnv, credRef, setCredRef,
    versionFor, updateBinding, toggleTool, selectAllTools, addCredential, removeCredential, testBinding, refreshCatalog, removeBinding,
  } = mcp;
  const catalog = catalogs[binding.id!] ?? [];
  const selected = binding.selectedRemoteToolNames ?? [];
  const version = versionFor(binding);
  const commandSummary = version
    ? version.transport === "stdio"
      ? `${version.executable ?? ""} ${(version.argv ?? []).join(" ")}`.trim()
      : version.endpoint ?? ""
    : "";

  return (
    <div key={binding.id} className={styles.card}>
      <div className={styles.cardHead}>
        <div className={styles.cardIdentity}>
          <div className={styles.cardTitle}>
            {version ? TRANSPORT_LABELS[version.transport] ?? version.transport : "MCP server"}
            <span className={styles.cardMeta}>
              rev {binding.revision ?? 0} · {selected.length} tools selected
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
            title={binding.required ? "Server is required: Run start fails when unreachable" : "Server is optional: Run start continues when unreachable"}
            onClick={() => updateBinding(binding, { required: !binding.required })}
            className={styles.requiredButton}
            data-required={binding.required || undefined}
          >
            {binding.required ? "required" : "optional"}
          </button>
          <button
            type="button"
            title="Edit credential references for this binding"
            onClick={() => setShowCreds((prev) => ({ ...prev, [binding.id!]: !prev[binding.id!] }))}
            className={styles.ghostButton}
          >
            Credentials
          </button>
          <button
            type="button"
            onClick={async () => {
              const next = !binding.desiredEnabled;
              if (next) {
                const summary = version
                  ? version.transport === "stdio"
                    ? `Command: ${commandSummary}`
                    : `Endpoint: ${version.endpoint ?? ""}`
                  : "";
                if (!(await confirmAction({
                  title: "Enable this MCP server?",
                  description: `${summary}\n\nOnly selected tools will be exposed to Runs.`,
                  confirmLabel: "Enable server",
                }))) return;
              }
              await updateBinding(binding, { desiredEnabled: next });
            }}
            className={styles.enableButton}
            data-enabled={binding.desiredEnabled || undefined}
          >
            {binding.desiredEnabled ? "Enabled" : "Disabled"}
          </button>
          <button type="button" title="Test connection" onClick={() => testBinding(binding)} className={styles.ghostButton}>
            Test
          </button>
          <button type="button" title="Refresh tool catalog" onClick={() => refreshCatalog(binding)} className={styles.ghostButton}>
            Refresh
          </button>
          <button type="button" title="Remove binding" onClick={() => removeBinding(binding)}
            className={`${styles.ghostButton} ${styles.danger}`}>
            Remove
          </button>
        </div>
      </div>
      {binding.desiredEnabled && (
        <div>
          <div className={styles.toolsHeader}>
            <span className={styles.toolsHint}>Selected tools (exact, frozen per Run)</span>
            {catalog.length > 0 && (
              <button type="button" onClick={() => selectAllTools(binding, catalog)} className={styles.ghostButton}>
                Select all current
              </button>
            )}
          </div>
          {catalog.length === 0 ? (
            <div className={styles.catalogEmpty}>
              No catalog yet — press Refresh to discover tools from the server.
            </div>
          ) : (
            <div>
              <input
                type="search"
                placeholder="Search tools…"
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
            Credential references (env: / file: / keyring: refs only — values never stored or shown)
          </div>
          {(Object.entries(binding.credentialRefs ?? {}) as [string, string][]).map(([env, ref]) => (
            <div key={env} className={styles.credentialRow}>
              <code className={styles.credentialEnv}>{env}</code>
              <code className={styles.credentialRef}>{ref}</code>
              <button type="button" onClick={() => removeCredential(binding, env)} className={styles.ghostButton}>
                Remove
              </button>
            </div>
          ))}
          {Object.keys(binding.credentialRefs ?? {}).length === 0 && (
            <div className={styles.credentialNote}>
              No binding-level credentials. The profile version&apos;s defaults apply.
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
              className={styles.ghostButton}
            >
              Add
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

export function McpCandidateSection({ mcp }: { mcp: ReturnType<typeof useMcpSettings> }) {
  const { profiles, candidates, bindings, versions, diffView, bindCandidate, openProfileDiff, archiveProfile } = mcp;

  if (candidates.length === 0 && profiles.length === 0) return null;
  return (
    <div>
      <div className={styles.sectionHeader}>Discovered servers</div>
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
                  Bound
                </span>
              ) : (
                <button type="button" onClick={() => bindCandidate(candidate)} className={styles.ghostButton}>
                  Bind
                </button>
              )}
              {candidate.alreadyBound && candidate.updateAvailable && (
                <button
                  type="button"
                  title="Project file changed; rebind to the new immutable version"
                  onClick={() => bindCandidate(candidate)}
                  className={`${styles.ghostButton} ${styles.accent}`}
                >
                  Update available
                </button>
              )}
              {candidate.alreadyBound && candidate.updateAvailable && (
                <button
                  type="button"
                  title="Show the read-only diff between the bound version and the project file"
                  onClick={() => openProfileDiff(candidate)}
                  className={styles.ghostButton}
                >
                  View diff
                </button>
              )}
              {candidate.sourceKind === "managed" && !candidate.alreadyBound && (() => {
                const profile = profiles.find(entry => entry.slug === candidate.slug && entry.lifecycleStatus === "active");
                return profile ? (
                  <button type="button" title="Archive MCP profile"
                    aria-label={`Archive MCP profile ${profile.displayName ?? profile.slug}`}
                    onClick={() => archiveProfile(profile)}
                    className={`${styles.ghostButton} ${styles.danger} ${styles.archiveButton}`}>
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
                  <button type="button" title="Archive MCP profile"
                    aria-label={`Archive MCP profile ${profile.displayName ?? profile.slug}`}
                    onClick={() => archiveProfile(profile)}
                    className={`${styles.ghostButton} ${styles.danger} ${styles.archiveButton}`}>
                    <Archive size={14} aria-hidden="true" />
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
