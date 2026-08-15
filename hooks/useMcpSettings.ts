"use client";

import { useCallback, useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import { diffText } from "@/lib/agent-flow";
import type { MCPCatalogEntry, MCPCandidate, MCPProjectBinding, MCPServerProfile, MCPServerProfileVersion } from "@/components/settings/types";

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

export function useMcpSettings(projectId: string | null, setError: (value: string | null) => void) {
  const [profiles, setProfiles] = useState<MCPServerProfile[]>([]);
  const [candidates, setCandidates] = useState<MCPCandidate[]>([]);
  const [bindings, setBindings] = useState<MCPProjectBinding[]>([]);
  const [catalogs, setCatalogs] = useState<Record<string, MCPCatalogEntry[]>>({});
  const [versions, setVersions] = useState<Record<string, MCPServerProfileVersion[]>>({});
  const [toolSearch, setToolSearch] = useState<Record<string, string>>({});
  const [showCreds, setShowCreds] = useState<Record<string, boolean>>({});
  const [credEnv, setCredEnv] = useState<Record<string, string>>({});
  const [credRef, setCredRef] = useState<Record<string, string>>({});
  const [diffView, setDiffView] = useState<{ slug: string; lines: string[]; digestTo: string } | null>(null);
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

  const versionById = useCallback((versionId: string | undefined): MCPServerProfileVersion | undefined => {
    if (!versionId) return undefined;
    for (const list of Object.values(versions)) {
      const v = list.find((x) => x.id === versionId);
      if (v) return v;
    }
    return undefined;
  }, [versions]);

  // openProfileDiff shows a read-only diff between the bound (frozen) version
  // and the project file candidate over the connection-defining fields both
  // sides can express (transport / executable / endpoint).
  const openProfileDiff = useCallback((candidate: MCPCandidate) => {
    const bound = versionById(candidate.boundVersionId);
    const oldText = JSON.stringify(
      bound ? {
        transport: bound.transport,
        ...(bound.transport === "stdio" ? { executable: bound.executable ?? "" } : { endpoint: bound.endpoint ?? "" }),
      } : { transport: "(unknown bound version)" },
      null, 2,
    );
    const newText = JSON.stringify(
      {
        transport: candidate.transport,
        ...(candidate.transport === "stdio" ? { executable: candidate.executable ?? "" } : { endpoint: candidate.endpoint ?? "" }),
      },
      null, 2,
    );
    setDiffView({
      slug: candidate.slug ?? "",
      lines: diffText(oldText, newText),
      digestTo: candidate.configDigest ?? "",
    });
  }, [versionById]);

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

  const archiveProfile = async (profile: MCPServerProfile) => {
    if (!profile.id || !window.confirm(`Archive MCP profile “${profile.displayName ?? profile.slug}”?`)) return;
    try {
      await apiFetch(`/v1/mcp/server-profiles/${encodeURIComponent(profile.id)}`, { method: "DELETE" });
      setError(null);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to archive MCP profile");
    }
  };

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

  const testBinding = async (binding: MCPProjectBinding) => {
    if (!projectId) return;
    try {
      await apiFetch(`/v1/projects/${projectId}/mcp/bindings/${binding.id}/test`, { method: "POST" });
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Test failed");
    }
  };

  const refreshCatalog = async (binding: MCPProjectBinding) => {
    if (!projectId) return;
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
  };

  const removeBinding = async (binding: MCPProjectBinding) => {
    if (!projectId) return;
    if (!window.confirm("Remove this MCP binding? Tools will no longer be available to Runs.")) return;
    try {
      await apiFetch(`/v1/projects/${projectId}/mcp/bindings/${binding.id}`, { method: "DELETE" });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to remove binding");
    }
  };

  return {
    profiles, candidates, bindings, catalogs, versions,
    toolSearch, setToolSearch, showCreds, setShowCreds, credEnv, setCredEnv, credRef, setCredRef,
    diffView, loading,
    showCreate, setShowCreate, transport, setTransport, displayName, setDisplayName,
    slug, setSlug, executable, setExecutable, argv, setArgv, endpoint, setEndpoint,
    versionFor, openProfileDiff,
    createProfile, bindCandidate, updateBinding, toggleTool, selectAllTools,
    addCredential, removeCredential, testBinding, refreshCatalog, removeBinding, archiveProfile,
  };
}
