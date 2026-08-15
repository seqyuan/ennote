"use client";

import { useCallback, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { SkillSearchResult } from "@/components/settings/types";

export function useSkillMarketplace(
  projectId: string | null,
  projectTrusted: boolean,
  reload: () => Promise<void>,
  setError: (value: string | null) => void,
): {
  query: string;
  setQuery: (value: string) => void;
  results: SkillSearchResult[];
  searching: boolean;
  searched: boolean;
  installScope: "global" | "project";
  setInstallScope: (scope: "global" | "project") => void;
  installing: string | null;
  search: () => Promise<void>;
  install: (pkg: string) => Promise<void>;
} {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SkillSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const [searched, setSearched] = useState(false);
  const [installScope, setInstallScope] = useState<"global" | "project">("global");
  const [installing, setInstalling] = useState<string | null>(null);

  const search = useCallback(async () => {
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
  }, [query, setError]);

  const install = useCallback(async (pkg: string) => {
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
      await reload();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Install failed");
    } finally {
      setInstalling(null);
    }
  }, [installScope, projectTrusted, projectId, setError, reload]);

  return {
    query, setQuery,
    results, searching, searched,
    installScope, setInstallScope,
    installing,
    search, install,
  };
}
