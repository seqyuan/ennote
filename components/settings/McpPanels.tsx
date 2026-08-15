"use client";

import { Archive } from "lucide-react";
import type { MCPCatalogEntry, MCPProjectBinding } from "@/components/settings/types";
import type { useMcpSettings } from "@/hooks/useMcpSettings";

const TRANSPORT_LABELS: Record<string, string> = {
  stdio: "stdio",
  streamable_http: "Streamable HTTP",
  legacy_sse: "HTTP + SSE",
};

const labelStyle: React.CSSProperties = {
  display: "flex", flexDirection: "column", gap: 3,
  fontSize: 11, color: "var(--text-muted)", fontWeight: 600,
};

const inputStyle: React.CSSProperties = {
  padding: "6px 8px", borderRadius: 6, border: "1px solid var(--border)",
  background: "var(--bg)", color: "var(--text)", fontSize: 12, width: "100%",
};

const ghostButtonStyle: React.CSSProperties = {
  padding: "4px 8px", borderRadius: 6, fontSize: 11,
  border: "1px solid var(--border)", background: "var(--bg)",
  color: "var(--text-muted)", cursor: "pointer",
};

export function McpCreateForm({ mcp }: { mcp: ReturnType<typeof useMcpSettings> }) {
  const {
    showCreate, transport, setTransport, displayName, setDisplayName,
    slug, setSlug, executable, setExecutable, argv, setArgv, endpoint, setEndpoint, createProfile,
  } = mcp;

  return (
    <>
      {showCreate && (
        <div style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 12, display: "flex", flexDirection: "column", gap: 8 }}>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
            <label style={labelStyle}>Display name
              <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} style={inputStyle} placeholder="PubMed" />
            </label>
            <label style={labelStyle}>Slug (fixed once bound)
              <input value={slug} onChange={(e) => setSlug(e.target.value)} style={inputStyle} placeholder="pubmed" />
            </label>
          </div>
          <div>
            <div style={{ display: "flex", gap: 6, marginBottom: 6 }}>
              {(["stdio", "streamable_http", "legacy_sse"] as const).map((kind) => (
                <button
                  key={kind}
                  type="button"
                  onClick={() => setTransport(kind)}
                  style={{
                    padding: "4px 10px", borderRadius: 6, fontSize: 11,
                    border: "1px solid var(--border)",
                    background: transport === kind ? "var(--bg-selected)" : "var(--bg)",
                    color: transport === kind ? "var(--text)" : "var(--text-muted)",
                    cursor: "pointer",
                  }}
                >
                  {TRANSPORT_LABELS[kind]}
                </button>
              ))}
            </div>
            {transport === "stdio" ? (
              <>
                <label style={labelStyle}>Executable
                  <input value={executable} onChange={(e) => setExecutable(e.target.value)} style={inputStyle} placeholder="/usr/bin/python3" />
                </label>
                <label style={labelStyle}>Arguments (space-separated)
                  <input value={argv} onChange={(e) => setArgv(e.target.value)} style={inputStyle} placeholder="/path/to/server.py" />
                </label>
              </>
            ) : (
              <label style={labelStyle}>Endpoint URL
                <input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} style={inputStyle} placeholder="https://example.com/mcp" />
              </label>
            )}
          </div>
          <button
            type="button"
            onClick={createProfile}
            disabled={!displayName.trim() || !slug.trim() || (transport === "stdio" ? !executable.trim() : !endpoint.trim())}
            style={{
              padding: "6px 12px", borderRadius: 6, border: "none",
              background: "var(--accent)", color: "#fff", fontSize: 12, cursor: "pointer", alignSelf: "flex-start",
            }}
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
    <div key={binding.id} style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 10, display: "flex", flexDirection: "column", gap: 8 }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)" }}>
            {version ? TRANSPORT_LABELS[version.transport] ?? version.transport : "MCP server"}
            <span style={{ color: "var(--text-dim)", fontWeight: 400, marginLeft: 6 }}>
              rev {binding.revision ?? 0} · {selected.length} tools selected
            </span>
          </div>
          {commandSummary && (
            <div style={{ fontSize: 11, color: "var(--text-muted)", marginTop: 2, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 360 }}>
              {commandSummary}
            </div>
          )}
        </div>
        <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
          <button
            type="button"
            title={binding.required ? "Server is required: Run start fails when unreachable" : "Server is optional: Run start continues when unreachable"}
            onClick={() => updateBinding(binding, { required: !binding.required })}
            style={{
              fontSize: 10, color: binding.required ? "var(--text)" : "var(--text-muted)",
              border: "1px solid var(--border)", borderRadius: 4, padding: "1px 6px",
              background: "var(--bg)", cursor: "pointer",
            }}
          >
            {binding.required ? "required" : "optional"}
          </button>
          <button
            type="button"
            title="Edit credential references for this binding"
            onClick={() => setShowCreds((prev) => ({ ...prev, [binding.id!]: !prev[binding.id!] }))}
            style={ghostButtonStyle}
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
                if (!window.confirm(`Enable this MCP server?\n${summary}\n\nOnly selected tools will be exposed to Runs.`)) return;
              }
              await updateBinding(binding, { desiredEnabled: next });
            }}
            style={{
              padding: "4px 10px", borderRadius: 6, fontSize: 11, border: "1px solid var(--border)",
              background: binding.desiredEnabled ? "var(--accent)" : "var(--bg)",
              color: binding.desiredEnabled ? "#fff" : "var(--text)",
              cursor: "pointer",
            }}
          >
            {binding.desiredEnabled ? "Enabled" : "Disabled"}
          </button>
          <button type="button" title="Test connection" onClick={() => testBinding(binding)} style={ghostButtonStyle}>
            Test
          </button>
          <button type="button" title="Refresh tool catalog" onClick={() => refreshCatalog(binding)} style={ghostButtonStyle}>
            Refresh
          </button>
          <button type="button" title="Remove binding" onClick={() => removeBinding(binding)} style={{ ...ghostButtonStyle, color: "#E11D48", borderColor: "var(--border)" }}>
            Remove
          </button>
        </div>
      </div>
      {binding.desiredEnabled && (
        <div>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 4 }}>
            <span style={{ fontSize: 11, color: "var(--text-dim)" }}>Selected tools (exact, frozen per Run)</span>
            {catalog.length > 0 && (
              <button type="button" onClick={() => selectAllTools(binding, catalog)} style={ghostButtonStyle}>
                Select all current
              </button>
            )}
          </div>
          {catalog.length === 0 ? (
            <div style={{ fontSize: 11, color: "var(--text-dim)" }}>
              No catalog yet — press Refresh to discover tools from the server.
            </div>
          ) : (
            <div>
              <input
                type="search"
                placeholder="Search tools…"
                value={toolSearch[binding.id!] ?? ""}
                onChange={(e) => setToolSearch((prev) => ({ ...prev, [binding.id!]: e.target.value }))}
                style={{ ...inputStyle, marginBottom: 6 }}
              />
              <div style={{ maxHeight: 220, overflowY: "auto", border: "1px solid var(--border)", borderRadius: 6 }}>
                <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 11 }}>
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
                        <tr key={entry.remoteName} style={{ borderBottom: "1px solid var(--border)" }}>
                          <td style={{ padding: "5px 8px", width: 28 }}>
                            <input
                              type="checkbox"
                              checked={selected.includes(entry.remoteName!)}
                              onChange={() => toggleTool(binding, entry.remoteName!)}
                              style={{ accentColor: "var(--accent)" }}
                            />
                          </td>
                          <td style={{ padding: "5px 8px", color: "var(--text)", fontWeight: 600 }}>{entry.remoteName}</td>
                          <td style={{ padding: "5px 8px", color: "var(--text-dim)", maxWidth: 260, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
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
        <div style={{ borderTop: "1px solid var(--border)", paddingTop: 8, display: "flex", flexDirection: "column", gap: 6 }}>
          <div style={{ fontSize: 11, color: "var(--text-dim)" }}>
            Credential references (env: / file: / keyring: refs only — values never stored or shown)
          </div>
          {(Object.entries(binding.credentialRefs ?? {}) as [string, string][]).map(([env, ref]) => (
            <div key={env} style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 11 }}>
              <code style={{ color: "var(--text)", minWidth: 120 }}>{env}</code>
              <code style={{ color: "var(--text-dim)", flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{ref}</code>
              <button type="button" onClick={() => removeCredential(binding, env)} style={ghostButtonStyle}>
                Remove
              </button>
            </div>
          ))}
          {Object.keys(binding.credentialRefs ?? {}).length === 0 && (
            <div style={{ fontSize: 11, color: "var(--text-dim)" }}>
              No binding-level credentials. The profile version&apos;s defaults apply.
            </div>
          )}
          <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
            <input
              value={credEnv[binding.id!] ?? ""}
              onChange={(e) => setCredEnv((prev) => ({ ...prev, [binding.id!]: e.target.value }))}
              placeholder="ENV_NAME"
              style={{ ...inputStyle, width: 140 }}
            />
            <input
              value={credRef[binding.id!] ?? ""}
              onChange={(e) => setCredRef((prev) => ({ ...prev, [binding.id!]: e.target.value }))}
              placeholder="env:MY_REF / file:... / keyring:..."
              style={{ ...inputStyle, flex: 1 }}
            />
            <button
              type="button"
              onClick={() => addCredential(binding)}
              disabled={!(credEnv[binding.id!] ?? "").trim() || !(credRef[binding.id!] ?? "").trim()}
              style={ghostButtonStyle}
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
      <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginBottom: 6 }}>Discovered servers</div>
      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        {candidates.map((candidate) => (
          <div key={`${candidate.sourceKind}:${candidate.slug}`} style={{ display: "flex", flexDirection: "column", gap: 4 }}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8, padding: "6px 10px", border: "1px solid var(--border)", borderRadius: 6 }}>
            <div style={{ minWidth: 0 }}>
              <span style={{ fontSize: 12, fontWeight: 600, color: "var(--text)" }}>{candidate.displayName ?? candidate.slug}</span>
              <span style={{ fontSize: 10, color: "var(--text-dim)", marginLeft: 6 }}>
                {candidate.transport} · {candidate.sourceKind}
              </span>
              {candidate.sourceKind === "project_file" && (
                <div style={{ fontSize: 10, color: "var(--text-dim)", marginTop: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 300 }}>
                  {candidate.sourceLocator}
                </div>
              )}
            </div>
            <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
              {candidate.alreadyBound ? (
                <span style={{ fontSize: 10, color: "var(--text-dim)", border: "1px solid var(--border)", borderRadius: 4, padding: "2px 8px" }}>
                  Bound
                </span>
              ) : (
                <button type="button" onClick={() => bindCandidate(candidate)} style={ghostButtonStyle}>
                  Bind
                </button>
              )}
              {candidate.alreadyBound && candidate.updateAvailable && (
                <button
                  type="button"
                  title="Project file changed; rebind to the new immutable version"
                  onClick={() => bindCandidate(candidate)}
                  style={{ ...ghostButtonStyle, color: "var(--accent)", borderColor: "var(--accent)" }}
                >
                  Update available
                </button>
              )}
              {candidate.alreadyBound && candidate.updateAvailable && (
                <button
                  type="button"
                  title="Show the read-only diff between the bound version and the project file"
                  onClick={() => openProfileDiff(candidate)}
                  style={ghostButtonStyle}
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
                    style={{ ...ghostButtonStyle, color: "#E11D48", padding: 4 }}>
                    <Archive size={14} aria-hidden="true" />
                  </button>
                ) : null;
              })()}
            </div>
          </div>
          {diffView && diffView.slug === candidate.slug && (
            <div style={{ border: "1px solid var(--border)", borderRadius: 6, padding: 8, marginTop: 6 }}>
              <div style={{ fontSize: 11, fontWeight: 700, color: "var(--text)", marginBottom: 4 }}>
                Read-only diff: bound version vs project file
                {diffView.digestTo && (
                  <span style={{ fontWeight: 400, color: "var(--text-dim)", marginLeft: 6, fontFamily: "var(--font-mono, monospace)", fontSize: 10 }}>
                    new config {diffView.digestTo.slice(0, 8)}
                  </span>
                )}
              </div>
              {diffView.lines.some((line) => line.startsWith("+ ") || line.startsWith("- ")) ? (
                <div style={{ maxHeight: 200, overflowY: "auto", fontSize: 10, fontFamily: "var(--font-mono, monospace)", color: "var(--text-muted)", whiteSpace: "pre-wrap" }}>
                  {diffView.lines.map((line, index) => (
                    <div key={index} style={{ color: line.startsWith("- ") ? "#E11D48" : line.startsWith("+ ") ? "#059669" : undefined }}>
                      {line}
                    </div>
                  ))}
                </div>
              ) : (
                <div style={{ fontSize: 10, color: "var(--text-dim)" }}>
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
            <div key={profile.id} style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8, padding: "6px 10px", border: "1px solid var(--border)", borderRadius: 6 }}>
              <div style={{ minWidth: 0 }}>
                <span style={{ fontSize: 12, fontWeight: 600, color: "var(--text)" }}>{profile.displayName}</span>
                <span style={{ fontSize: 10, color: "var(--text-dim)", marginLeft: 6 }}>
                  {profile.slug} · v{profile.latestVersion ?? 0} · managed
                </span>
              </div>
              <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
                {bound ? (
                  <span style={{ fontSize: 10, color: "var(--text-dim)", border: "1px solid var(--border)", borderRadius: 4, padding: "2px 8px" }}>
                    Bound
                  </span>
                ) : (
                  <button type="button" title="Archive MCP profile"
                    aria-label={`Archive MCP profile ${profile.displayName ?? profile.slug}`}
                    onClick={() => archiveProfile(profile)}
                    style={{ ...ghostButtonStyle, color: "#E11D48", padding: 4 }}>
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
