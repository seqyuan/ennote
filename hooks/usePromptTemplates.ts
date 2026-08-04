"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type TemplateSummary = components["schemas"]["PromptTemplateSummary"];
type TemplateDiagnostic = components["schemas"]["PromptTemplateDiagnostic"];

interface CatalogState {
  templates: TemplateSummary[];
  diagnostics: TemplateDiagnostic[];
  loading: boolean;
  error: string | null;
}

export function usePromptTemplates(projectId: string | null) {
  const [state, setState] = useState<CatalogState>({
    templates: [], diagnostics: [], loading: false, error: null,
  });
  const abortRef = useRef<AbortController | null>(null);

  const fetchCatalog = useCallback(async () => {
    if (!projectId) {
      setState({ templates: [], diagnostics: [], loading: false, error: null });
      return;
    }

    // Cancel any in-flight request.
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    setState((prev) => ({ ...prev, loading: true, error: null }));
    try {
      const data = await apiFetch<{ templates: TemplateSummary[]; diagnostics: TemplateDiagnostic[] }>(
        `/v1/projects/${encodeURIComponent(projectId)}/prompt-templates`,
        { signal: controller.signal },
      );
      if (controller.signal.aborted) return;
      setState({ templates: data.templates, diagnostics: data.diagnostics, loading: false, error: null });
    } catch (err: unknown) {
      if (controller.signal.aborted) return;
      setState((prev) => ({
        ...prev,
        loading: false,
        error: (err as Error).message ?? "Failed to load prompt templates",
      }));
    }
  }, [projectId]);

  // Load the effective catalog as soon as a project becomes active. The
  // command-panel refresh below still catches external file changes.
  useEffect(() => {
    if (projectId) void fetchCatalog();
  }, [fetchCatalog, projectId]);

  // Clean up on unmount.
  useEffect(() => {
    return () => { abortRef.current?.abort(); };
  }, []);

  return { ...state, refresh: fetchCatalog };
}
