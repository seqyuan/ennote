"use client";

import { useCallback, useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { AnnotatedSkill, SkillListResult, SkillRoot } from "@/components/settings/types";

type SkillDiagnostic = { level?: string; message?: string; relPath?: string; source?: string };

export function useSkillCatalog(projectId: string | null, setError: (value: string | null) => void): {
  skills: AnnotatedSkill[];
  setSkills: React.Dispatch<React.SetStateAction<AnnotatedSkill[]>>;
  diagnostics: SkillDiagnostic[];
  projectTrusted: boolean;
  loading: boolean;
  roots: SkillRoot[];
  setRoots: React.Dispatch<React.SetStateAction<SkillRoot[]>>;
  load: () => Promise<void>;
} {
  const [skills, setSkills] = useState<AnnotatedSkill[]>([]);
  const [diagnostics, setDiagnostics] = useState<SkillDiagnostic[]>([]);
  const [projectTrusted, setProjectTrusted] = useState(false);
  const [loading, setLoading] = useState(false);
  const [roots, setRoots] = useState<SkillRoot[]>([]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const params = projectId ? `?projectID=${encodeURIComponent(projectId)}` : "";
      const result = await apiFetch<SkillListResult>(`/v1/skills${params}`);
      setSkills(result.skills ?? []);
      setDiagnostics(result.diagnostics ?? []);
      setProjectTrusted(Boolean(result.projectResourcesLoaded));
      const rootsData = await apiFetch<{ items: SkillRoot[] }>("/v1/skills/roots");
      setRoots(rootsData.items ?? []);
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

  return { skills, setSkills, diagnostics, projectTrusted, loading, roots, setRoots, load };
}
