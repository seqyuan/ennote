"use client";

import { useCallback, useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { MCPCatalogEntry, MCPCandidate, MCPProjectBinding, MCPServerProfile, MCPServerProfileVersion } from "@/components/settings/types";

const TRANSPORT_LABELS: Record<string, string> = {
  stdio: "stdio",
  streamable_http: "Streamable HTTP",
  legacy_sse: "HTTP + SSE",
};

interface ProfileVersionPayload {
  transport: string;
  executable?: string;
  argv?: string[];
  endpoint?: string;
  envLiterals?: Record<string, string>;
  envCredentials?: Record<string, string>;
  cwd?: string;
  timeoutMs?: number;
  networkPolicy?: string;
}

export function McpSettings({ projectId, setError }: {
  projectId: string | null;
  setError: (value: string | null) => void;
}) {
  const [profiles, setProfiles] = useState<MCPServerProfile[]>([]);
  const [candidates, setCandidates] = useState<MCPCandidate[]>([]);
  const [bindings, setBindings] = useState<MCPProjectBinding[]>([]);
  const [catalogs, setCatalogs] = useState<Record<string, MCPCatalogEntry[]>>({});
  const [versions, setVersions] = useState<Record<string, MCPServerProfileVersion[]>>({});
  const [toolSearch, setToolSearch] = useState<Record<string, string>>({});
  const [showCreds, setShowCreds] = useState<Record<string, boolean>>({});
  const [credEnv, setCredEnv] = useState<Record<string, string>>({});
  const [credRef, setCredRef] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [transport, setTransport] = useState("stdio");
  const [displayName, setDisplayName] = useState("");
  const [slug, setSlug] = useState("");
  const [executable, setExecutable] = useState("");
  const [argv, setArgv] = useState("");
  const [endpoint, setEndpoint] = useState("");

  const loadVersions = useCallback(async (profileList: MCPServerProfile[]) => {
    const map: Record<string, MCPServerProfileVersion[]> = {};
    await Promise.all(
      profileList.map(async (p) => {
        try {
          map[p.id!] = await apiFetch<MCPServerProfileVersion[]>(
            `/v1/mcp/server-profiles/${p.id}/versions`,
          );
        } catch {
          // Version list unavailable; keep empty.
        }
      }),
    );
    setVersions(map);
  }, []);

  const versionFor = useCallback(
    (binding: MCPProjectBinding): MCPServerProfileVersion | undefined => {
      for (const list of Object.values(versions)) {
        const v = list.find((x) => x.id === binding.profileVersionId);
        if (v) return v;
      }
      return undefined;
    },
    [versions],
  );

  const refresh = useCallback(async () => {
    if (!projectId) return;
    setLoading(true);
    try {
      const [profileList, bindingList, candidateList] = await Promise.all([
        apiFetch<MCPServerProfile[]>("/v1/mcp/server-profiles"),
        apiFetch<MCPProjectBinding[]>(`/v1/projects/${projectId}/mcp/bindings`),
        apiFetch<MCPCandidate[]>(`/v1/projects/${projectId}/mcp/candidates`),
      ]);
      setProfiles(profileList);
      setBindings(bindingList);
      setCandidates(candidateList);
      void loadVersions(profileList);
      // Load cached catalogs for enabled bindings.
      const catalogMap: Record<string, MCPCatalogEntry[]> = {};
      for (const binding of bindingList) {
        if (!binding.desiredEnabled) continue;
        try {
          catalogMap[binding.id!] = await apiFetch<MCPCatalogEntry[]>(
            `/v1/projects/${projectId}/mcp/bindings/${binding.id}/catalog`,
          );
        } catch {
          // Catalog may not be discovered yet; keep it empty.
        }
      }
      setCatalogs(catalogMap);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load MCP settings");
    } finally {
      setLoading(false);
    }
  }, [projectId, setError, loadVersions]);

  useEffect(() => {
    let cancelled = false;
    if (!projectId) return;
    void Promise.all([
      apiFetch<MCPServerProfile[]>("/v1/mcp/server-profiles"),
      apiFetch<MCPProjectBinding[]>(`/v1/projects/${projectId}/mcp/bindings`),
      apiFetch<MCPCandidate[]>(`/v1/projects/${projectId}/mcp/candidates`).catch(() => {
        throw new Error("Failed to load discovered servers");
      }),
    ])
      .then(async ([profileList, bindingList, candidateList]) => {
        if (cancelled) return;
        setProfiles(profileList);
        setBindings(bindingList);
        setCandidates(candidateList);
        void loadVersions(profileList);
        const catalogMap: Record<string, MCPCatalogEntry[]> = {};
        for (const binding of bindingList) {
          if (!binding.desiredEnabled) continue;
          try {
            catalogMap[binding.id!] = await apiFetch<MCPCatalogEntry[]>(
              `/v1/projects/${projectId}/mcp/bindings/${binding.id}/catalog`,
            );
          } catch {
            // Catalog may not be discovered yet; keep it empty.
          }
        }
        if (cancelled) return;
        setCatalogs(catalogMap);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load MCP settings");
      });
    return () => {
      cancelled = true;
    };
  }, [projectId, setError, loadVersions]);

  const createProfile = async () => {
    try {
      const profile = await apiFetch<MCPServerProfile>("/v1/mcp/server-profiles", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ displayName, slug, sourceKind: "managed" }),
      });
      const versionPayload: ProfileVersionPayload = { transport };
      if (transport === "stdio") {
        versionPayload.executable = executable.trim();
        const parsedArgv = argv.split(/\s+/).filter(Boolean);
        if (parsedArgv.length > 0) versionPayload.argv = parsedArgv;
      } else {
        versionPayload.endpoint = endpoint.trim();
      }
      await apiFetch<MCPServerProfileVersion>(`/v1/mcp/server-profiles/${profile.id}/versions`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(versionPayload),
      });
      setShowCreate(false);
      setDisplayName("");
      setSlug("");
      setExecutable("");
      setArgv("");
      setEndpoint("");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create MCP profile");
    }
  };

  const bindCandidate = async (candidate: MCPCandidate) => {
    if (!projectId) return;
    try {
      // When an update is available the bound version is stale; always
      // re-materialize from the project file so a NEW immutable version is
      // created and bound (never rewrite the old one).
      if (candidate.sourceKind === "project_file" && !candidate.boundVersionId && !candidate.updateAvailable) {
        await apiFetch<MCPProjectBinding>(`/v1/projects/${projectId}/mcp/bindings/from-candidate`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ slug: candidate.slug, transport: candidate.transport }),
        });
      } else if (candidate.boundVersionId && !candidate.updateAvailable) {
        await apiFetch<MCPProjectBinding>(`/v1/projects/${projectId}/mcp/bindings`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ profileVersionId: candidate.boundVersionId }),
        });
      } else {
        // updateAvailable (or unbound project-file): materialize the new version.
        await apiFetch<MCPProjectBinding>(`/v1/projects/${projectId}/mcp/bindings/from-candidate`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ slug: candidate.slug, transport: candidate.transport }),
        });
      }
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to bind MCP candidate");
    }
  };

  const updateBinding = async (binding: MCPProjectBinding, patch: Record<string, unknown>) => {
    if (!projectId) return;
    try {
      await apiFetch<MCPProjectBinding>(
        `/v1/projects/${projectId}/mcp/bindings/${binding.id}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(patch),
        },
      );
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update MCP binding");
    }
  };

  const toggleTool = async (binding: MCPProjectBinding, remoteName: string) => {
    const selected = binding.selectedRemoteToolNames ?? [];
    const next = selected.includes(remoteName)
      ? selected.filter((name) => name !== remoteName)
      : [...selected, remoteName];
    await updateBinding(binding, { selectedRemoteToolNames: next });
  };

  const selectAllTools = async (binding: MCPProjectBinding, catalog: MCPCatalogEntry[]) => {
    await updateBinding(binding, { selectedRemoteToolNames: catalog.map((entry) => entry.remoteName) });
  };

  const addCredential = async (binding: MCPProjectBinding) => {
    const env = (credEnv[binding.id!] ?? "").trim();
    const ref = (credRef[binding.id!] ?? "").trim();
    if (!env || !ref) return;
    const next = { ...(binding.credentialRefs ?? {}) };
    next[env] = ref;
    await updateBinding(binding, { credentialRefs: next });
    setCredEnv((prev) => ({ ...prev, [binding.id!]: "" }));
    setCredRef((prev) => ({ ...prev, [binding.id!]: "" }));
  };

  const removeCredential = async (binding: MCPProjectBinding, env: string) => {
    const next = { ...(binding.credentialRefs ?? {}) };
    delete next[env];
    await updateBinding(binding, { credentialRefs: next });
  };

  if (!projectId) {
    return <div style={{ fontSize: 12, color: "var(--text-dim)" }}>Open a project to manage MCP servers.</div>;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      {/* Section header */}
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <div>
          <div style={{ fontSize: 13, fontWeight: 700, color: "var(--text)" }}>MCP servers</div>
          <div style={{ fontSize: 11, color: "var(--text-dim)", marginTop: 2 }}>
            Tools-first client. Servers are discovered, tested, then explicitly enabled per project.
          </div>
        </div>
        <button
          type="button"
          onClick={() => setShowCreate((value) => !value)}
          style={{
            padding: "5px 10px", borderRadius: 6, border: "1px solid var(--border)",
            background: "var(--bg)", color: "var(--text)", fontSize: 12, cursor: "pointer",
          }}
        >
          {showCreate ? "Cancel" : "+ Add server"}
        </button>
      </div>

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

      {/* Bindings */}
      <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
        {bindings.length === 0 && !loading && (
          <div style={{ fontSize: 12, color: "var(--text-dim)" }}>No MCP servers bound to this project yet.</div>
        )}
        {bindings.map((binding) => {
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
                  <button
                    type="button"
                    title="Test connection"
                    onClick={async () => {
                      try {
                        await apiFetch(`/v1/projects/${projectId}/mcp/bindings/${binding.id}/test`, { method: "POST" });
                        setError(null);
                      } catch (err) {
                        setError(err instanceof Error ? err.message : "Test failed");
                      }
                    }}
                    style={ghostButtonStyle}
                  >
                    Test
                  </button>
                  <button
                    type="button"
                    title="Refresh tool catalog"
                    onClick={async () => {
                      try {
                        const entries = await apiFetch<MCPCatalogEntry[]>(
                          `/v1/projects/${projectId}/mcp/bindings/${binding.id}/catalog/refresh`,
                          { method: "POST" },
                        );
                        setCatalogs((prev) => ({ ...prev, [binding.id!]: entries }));
                        setError(null);
                      } catch (err) {
                        setError(err instanceof Error ? err.message : "Catalog refresh failed");
                      }
                    }}
                    style={ghostButtonStyle}
                  >
                    Refresh
                  </button>
                  <button
                    type="button"
                    title="Remove binding"
                    onClick={async () => {
                      if (!window.confirm("Remove this MCP binding? Tools will no longer be available to Runs.")) return;
                      try {
                        await apiFetch(`/v1/projects/${projectId}/mcp/bindings/${binding.id}`, { method: "DELETE" });
                        await refresh();
                      } catch (err) {
                        setError(err instanceof Error ? err.message : "Failed to remove binding");
                      }
                    }}
                    style={{ ...ghostButtonStyle, color: "#E11D48", borderColor: "var(--border)" }}
                  >
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
        })}
      </div>

      {/* Discovered candidates (project file, bundled, managed) */}
      {(candidates.length > 0 || profiles.length > 0) && (
        <div>
          <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginBottom: 6 }}>Discovered servers</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {candidates.map((candidate) => (
              <div key={`${candidate.sourceKind}:${candidate.slug}`} style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8, padding: "6px 10px", border: "1px solid var(--border)", borderRadius: 6 }}>
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
                </div>
              </div>
            ))}
            {/* Managed profiles already in the library but not listed as candidates */}
            {candidates.length === 0 && profiles.map((profile) => {
              const bound = bindings.some((binding) => (binding.profileVersionId?.startsWith(profile.id!) ?? false));
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
                      <span style={{ fontSize: 10, color: "var(--text-dim)" }}>Use “+ Add server” to configure</span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}
      {loading && <div style={{ fontSize: 11, color: "var(--text-dim)" }}>Loading…</div>}
    </div>
  );
}

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
