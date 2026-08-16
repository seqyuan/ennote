"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { RoleSummary } from "@/components/settings/types";
import { errorMessage } from "@/lib/provider-errors";
import { apiFetch } from "@/lib/worker-api.client";
import type { usePromptTemplates } from "@/hooks/usePromptTemplates";

type ExpandResponse = {
  case: "matched"; name: string; text: string; diagnostics: { level: string; code: string; message: string }[];
} | {
  case: "not_found"; name: string; diagnostics: { level: string; code: string; message: string }[];
} | {
  case: "invalid_invocation"; diagnostics: never[];
};

export type ExpandCtx = { project: string; session: string | null; draftVersion: number };
export type ExpansionOutcome =
  | { status: "matched"; text: string; diagnostics: { level: string; code: string; message: string }[] }
  | { status: "empty" }
  | { status: "fallthrough" };

export type PromptExpansionDeps = {
  selectedProject: string | null;
  selectedSession: string | null;
  input: string; // commandPanelOpen reads the input VALUE, so it is a dep (P0-1)
  promptCatalog: ReturnType<typeof usePromptTemplates>;
  roles: RoleSummary[]; // commandPanel @role matching
  setInput: (value: string) => void;
  setError: (error: string | null) => void;
};

/**
 * Command expansion + command panel: owns the versioned input setter, the
 * slash-template expansion gate, the command panel visibility, and the graph
 * catalog. Graph invocation (invokeAgentFlow) stays in the caller because it is
 * a chat action, not expansion mechanics.
 */
export function usePromptExpansion(deps: PromptExpansionDeps): {
  setInputVersioned: (value: string) => void;
  expandDraftOrFallback: (draft: string, ctx: ExpandCtx, errorLabel: string) => Promise<ExpansionOutcome | null>;
  expandedVersion: number | null;
  draftVersion: number;
  flowCatalog: { name: string; version?: number }[];
  commandPanelOpen: boolean;
  promptPanelDismissed: boolean;
  setPromptPanelDismissed: (dismissed: boolean) => void;
  expanding: boolean;
  expandDiag: string | null;
  setExpandDiag: (diag: string | null) => void;
} {
  const { selectedProject, selectedSession, input, promptCatalog, roles, setInput, setError } = deps;

  const [draftVersion, setDraftVersion] = useState(0);
  const [expandedVersion, setExpandedVersion] = useState<number | null>(null);
  const [expanding, setExpanding] = useState(false);
  const [expandDiag, setExpandDiag] = useState<string | null>(null);
  const [promptPanelDismissed, setPromptPanelDismissed] = useState(false);
  const expandAbortRef = useRef<AbortController | null>(null);
  const [flowCatalog, setFlowCatalog] = useState<{ name: string; version?: number }[]>([]);

  const setInputVersioned = useCallback((value: string) => {
    setInput(value);
    setDraftVersion((v) => v + 1);
    setExpandedVersion(null);
    // Keep an explicit dismissal while editing one slash token, then re-arm
    // once the input is no longer eligible for the command panel.
    if (!value.startsWith("/") || /\s/.test(value.slice(1))) {
      setPromptPanelDismissed(false);
    }
  }, [setInput]);

  // Graph catalog for @graph addressing; refreshed on mount and whenever the
  // command panel transitions closed -> open.
  const refreshFlowCatalog = useCallback(async () => {
    try {
      const graphs = await apiFetch<Array<{ id: string; latestVersion?: number }>>("/v1/graphs");
      setFlowCatalog((graphs ?? [])
        .filter((graph) => (graph.latestVersion ?? 0) > 0)
        .map((graph) => ({ name: graph.id, version: graph.latestVersion })));
    } catch { /* panel is a convenience; failures surface elsewhere */ }
  }, []);

  useEffect(() => {
    const t0 = window.setTimeout(() => void refreshFlowCatalog(), 0);
    return () => window.clearTimeout(t0);
  }, [refreshFlowCatalog]);

  const commandPanelOpen = Boolean(
    selectedProject
    && !input.slice(1).match(/[\s]/)
    && (
      (input.startsWith("/") && promptCatalog.templates.length > 0)
      || (input.startsWith("@role") && roles.length > 0)
      || (input.startsWith("@graph") && flowCatalog.length > 0)
      || (input.startsWith("@") && (roles.length > 0 || flowCatalog.length > 0))
    ),
  );
  const wasPanelOpen = useRef(false);
  useEffect(() => {
    if (commandPanelOpen && !wasPanelOpen.current) {
      void promptCatalog.refresh();
      void refreshFlowCatalog();
    }
    wasPanelOpen.current = commandPanelOpen;
  }, [commandPanelOpen, promptCatalog, refreshFlowCatalog]);

  const handleExpand = useCallback(async (invocation: string, projectId: string): Promise<ExpandResponse | null> => {
    expandAbortRef.current?.abort();
    const controller = new AbortController();
    expandAbortRef.current = controller;
    try {
      const data = await apiFetch<ExpandResponse>(
        `/v1/projects/${encodeURIComponent(projectId)}/prompt-templates/expand`,
        { method: "POST", body: JSON.stringify({ invocation }), signal: controller.signal },
      );
      if (controller.signal.aborted) return null;
      return data;
    } catch (err: unknown) {
      if (controller.signal.aborted) return null;
      throw err;
    }
  }, []);

  // Shared slash-expansion gate for submit and steer: entry resets, expansion
  // request, stale-guard and finally-reset live here in one copy. Caller-specific
  // behavior (expandDiag on submit, fallback action) is left to the caller.
  const expandDraftOrFallback = useCallback(async (
    draft: string,
    ctx: ExpandCtx,
    errorLabel: string,
  ): Promise<ExpansionOutcome | null> => {
    setExpanding(true);
    setExpandDiag(null);
    try {
      const result = await handleExpand(draft, ctx.project);
      if (!result) return null; // aborted
      // Stale-guard: context changed during request.
      if (ctx.project !== selectedProject || ctx.session !== selectedSession || ctx.draftVersion !== draftVersion) {
        return null;
      }
      switch (result.case) {
        case "matched": {
          const text = result.text.trim();
          if (!text) {
            setError("Expanded prompt is empty.");
            return { status: "empty" };
          }
          setInput(text);
          setDraftVersion((v) => v + 1);
          setExpandedVersion(draftVersion + 1);
          return { status: "matched", text, diagnostics: result.diagnostics };
        }
        case "not_found":
        case "invalid_invocation":
          return { status: "fallthrough" };
      }
    } catch (err: unknown) {
      setError(errorMessage(err, errorLabel));
      return null;
    } finally {
      setExpanding(false);
    }
  }, [setError, selectedProject, selectedSession, draftVersion, handleExpand, setInput, setDraftVersion, setExpandedVersion, setExpanding, setExpandDiag]);

  return {
    setInputVersioned,
    expandDraftOrFallback,
    expandedVersion,
    draftVersion,
    flowCatalog,
    commandPanelOpen,
    promptPanelDismissed,
    setPromptPanelDismissed,
    expanding,
    expandDiag,
    setExpandDiag,
  };
}
