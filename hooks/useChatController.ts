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

  // Model-catalog resolver, hoisted so the session data hooks can apply it
  // inside the projection (no second enrichment pass re-cloning nodes).
  const modelNameById = useMemo(
    () => new Map(settings.models.map((model) => [model.id, model.displayName || model.modelName])),
    [settings.models],
  );
  const resolveModel = useCallback(
    (modelProfileId?: string, apiModel?: string) => (modelProfileId ? modelNameById.get(modelProfileId) : undefined) ?? apiModel,
    [modelNameById],
  );

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
  const messagesData = useSessionMessages(selectedSession, activeBranchId, { resolveModel });
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

  const history = {
    sessionId: selectedSession,
    activeBranchId,
    activeLeafMessageId,
    messages: messagesData.messages,
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
