"use client";

import { useCallback, useMemo } from "react";
import type { Session } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";
import { useAgentSession } from "@/hooks/useAgentSession";
import { useSessionMessages } from "@/hooks/useSessionMessages";
import { useRunRecovery } from "@/hooks/useRunRecovery";
import { useSessionBranches } from "@/hooks/useSessionBranches";
import { useChatComposer } from "@/hooks/useChatComposer";
import { useSessionStats } from "@/hooks/useSessionStats";
import type { usePromptTemplates } from "@/hooks/usePromptTemplates";
import type { useSettingsProfiles } from "@/hooks/useSettingsProfiles";

export type {
  ChatActions, ChatController, ComposerView, HistoryView, RunView, BranchView,
} from "./chat-controller-types";

// The session data hooks (useSessionMessages / useAgentSession / useRunRecovery /
// useSessionBranches) are owned inside useChatController and surfaced as the
// composed history/run/branches views. AppShell only passes navigation-level
// inputs (selectedSession, sessionRecord, promptCatalog, settings, replaceSession).
export type ChatControllerDeps = {
  selectedSession: string | null;
  sessionRecord: Session | null | undefined;
  selectedProject: string | null;
  promptCatalog: ReturnType<typeof usePromptTemplates>;
  settings: ReturnType<typeof useSettingsProfiles>;
  replaceSession: (s: Session) => void;
};

export function useChatController(deps: ChatControllerDeps) {
  const {
    selectedSession, sessionRecord, selectedProject, promptCatalog, settings, replaceSession,
  } = deps;

  // Session lineage derivations come from the session record (owned by the
  // caller's project-sessions store).
  const activeBranchId = sessionRecord?.activeBranchId;
  const activeLeafMessageId = sessionRecord?.activeLeafMessageId;
  const updateSession = useCallback((session: Session) => replaceSession(session), [replaceSession]);
  const refreshSelectedSession = useCallback(async () => {
    if (!selectedSession) return null;
    const current = await apiFetch<Session>(`/v1/sessions/${encodeURIComponent(selectedSession)}`);
    replaceSession(current);
    return current;
  }, [replaceSession, selectedSession]);

  // Session data hooks (owned here; AppShell only receives the composed views).
  const messagesData = useSessionMessages(selectedSession, activeBranchId);
  const agent = useAgentSession({
    sessionId: selectedSession, lineageId: activeBranchId, appendMessage: messagesData.appendTransient,
    upsertMessage: messagesData.upsertTransient, refreshLatest: messagesData.refreshLatest,
    refreshSession: refreshSelectedSession,
    activeRun: messagesData.activeRun, pendingApproval: messagesData.pendingApproval,
  });
  const recoveryData = useRunRecovery(selectedSession, activeBranchId, agent.activeRunID);
  const sessionStats = useSessionStats(selectedSession, agent.activeRunID);
  const branchesData = useSessionBranches({ sessionId: selectedSession, activeBranchId, onSessionUpdated: updateSession });

  // Composer domain: state + actions in its own hook, driven by a runtime
  // slice of the session-data hooks above.
  const { composer, actions } = useChatComposer({
    selectedSession,
    selectedProject,
    sessionRecord,
    promptCatalog,
    settings,
    runtime: {
      activeRunID: agent.activeRunID,
      activeRunRecord: agent.activeRun,
      setStatus: agent.setStatus,
      setError: agent.setError,
      watchRun: agent.watchRun,
      steer: agent.steer,
      followUp: agent.followUp,
      appendTransient: messagesData.appendTransient,
      refreshSelectedSession,
    },
  });

  const activeRunID = agent.activeRunID;
  const { decideApproval: decideApprovalOnRun } = agent;
  const { createBranch: createBranchOnBranch } = branchesData;

  const createBranch = useCallback(async (messageId: string) => {
    if (!activeRunID) await createBranchOnBranch(messageId);
  }, [activeRunID, createBranchOnBranch]);
  const activateBranch = async (branchId: string) => {
    if (!activeRunID) await branchesData.activateBranch(branchId);
  };
  // Stable callbacks for the memoized render tree: ConversationTurn and
  // AssistantMessage skip re-render only when these keep their identity.
  const decideApproval = useCallback(
    (decision: Parameters<typeof decideApprovalOnRun>[0]) => void decideApprovalOnRun(decision),
    [decideApprovalOnRun],
  );
  const createBranchForTree = useCallback(
    (messageId: string) => void createBranch(messageId),
    [createBranch],
  );
  const retryRun = useCallback(async () => {
    const run = await recoveryData.retry();
    if (run) void agent.watchRun(run);
  }, [recoveryData, agent]);

  const clearError = useCallback(() => {
    agent.setError(null);
    recoveryData.clearError();
    branchesData.clearError();
  }, [agent, recoveryData, branchesData]);

  const error = agent.error ?? recoveryData.error ?? branchesData.error;

  // Resolve the run-frozen modelProfileId into a display name from the catalog
  // (falling back to the raw apiModel), so assistant speaker labels can show
  // which model produced a reply without each message row knowing the catalog.
  const modelNameById = useMemo(
    () => new Map(settings.models.map((model) => [model.id, model.displayName || model.modelName])),
    [settings.models],
  );

  // Attribution for transient (streaming) assistant steps, which carry no
  // speaker. Inherit the active run's speaker snapshot + resolved model so the
  // speaker label and model name render in real time, not only after the reply
  // is committed as a canonical message.
  const activeRunAttribution = useMemo(() => {
    const run = agent.activeRun;
    if (!run) return null;
    const effective = (run.effectiveConfig ?? {}) as Record<string, unknown>;
    const requested = (run.requestedConfig ?? {}) as Record<string, unknown>;
    const modelProfileId = (typeof effective.modelProfileId === "string" && effective.modelProfileId)
      ? effective.modelProfileId
      : (typeof requested.modelProfileId === "string" ? requested.modelProfileId : undefined);
    const apiModel = (typeof effective.apiModel === "string" && effective.apiModel)
      ? effective.apiModel
      : (typeof requested.apiModel === "string" ? requested.apiModel : undefined);
    const modelName = (modelProfileId ? modelNameById.get(modelProfileId) : undefined) ?? apiModel;
    return { speaker: run.speakerSnapshot, modelProfileId, apiModel, modelName };
  }, [agent.activeRun, modelNameById]);

  const messages = useMemo(
    () => messagesData.messages.map((node) => {
      if (node.kind !== "turn") return node;
      // Only enrich un-enriched assistant steps; skip already-resolved ones so
      // streaming updates (transient appends) don't re-clone the whole tree.
      let changed = false;
      const steps = node.steps.map((step) => {
        if (step.kind !== "assistant") return step;
        if (!step.speaker) {
          if (!activeRunAttribution) return step;
          changed = true;
          const { speaker, modelProfileId, apiModel, modelName } = activeRunAttribution;
          return { ...step, speaker: { ...speaker, modelProfileId, apiModel, modelName } };
        }
        if (step.speaker.modelName) return step;
        const resolved = step.speaker.modelProfileId ? modelNameById.get(step.speaker.modelProfileId) : undefined;
        const modelName = resolved ?? step.speaker.apiModel;
        if (!modelName) return step;
        changed = true;
        return { ...step, speaker: { ...step.speaker, modelName } };
      });
      return changed ? { ...node, steps } : node;
    }),
    [messagesData.messages, modelNameById, activeRunAttribution],
  );

  const history = {
    sessionId: selectedSession,
    activeBranchId,
    activeLeafMessageId,
    messages,
    loading: messagesData.loading,
    loadingOlder: messagesData.loadingOlder,
    error: messagesData.historyError,
    hasMore: messagesData.hasMore,
    loadOlder: messagesData.loadOlder,
  };

  const run = {
    activeRun: agent.activeRunID,
    activeRunStatus: agent.activeRun?.status,
    status: agent.status,
    usage: agent.usage,
    contextUsage: agent.contextUsage,
    stats: sessionStats,
    compacting: agent.compacting,
    pendingApproval: agent.pendingApproval,
    resolvingApproval: agent.resolvingApproval,
    decideApproval,
    cancel: () => void agent.cancel(),
    pendingFollowUps: agent.pendingFollowUps,
    recovery: recoveryData.recovery,
    retrying: recoveryData.retrying,
    retryRun: () => void retryRun(),
    error: agent.error,
    setError: agent.setError,
    clearError: () => agent.setError(null),
  };

  const branches = {
    branches: branchesData.branches,
    activeBranchId,
    loading: branchesData.loading,
    changing: branchesData.changing,
    error: branchesData.error,
    createBranch: createBranchForTree,
    activateBranch: (branchId: string) => void activateBranch(branchId),
  };

  return {
    history,
    run,
    branches,
    composer,
    actions,
    error,
    clearError,
  };
}
