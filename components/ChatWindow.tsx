"use client";

import { X } from "lucide-react";
import { useCallback, useEffect, useRef } from "react";
import { Composer } from "@/components/Composer";
import { EmptyHero, HeroGlow } from "@/components/EmptyHero";
import { HeroProjectChip } from "@/components/HeroProjectChip";
import { BackgroundDelegationStrip } from "@/components/BackgroundDelegationStrip";
import { CompactionPromptBar } from "@/components/CompactionPromptBar";
import { ConversationTimeline } from "@/components/ConversationTimeline";
import { StreamingStatusBar } from "@/components/StreamingStatusBar";
import type { BranchView, ChatActions, ComposerView, HistoryView, RunView } from "@/hooks/useChatController";
import type { components } from "@/lib/worker-api.gen";
import { useProjectSelector } from "@/hooks/useProjectSelector";
import type { SidebarProject } from "./ProjectMenu";

type RunRecovery = components["schemas"]["RunRecovery"];

interface ChatWindowProps {
  history: HistoryView;
  run: RunView;
  branches: BranchView;
  composer: ComposerView;
  actions: ChatActions;
  error: string | null;
  clearError: () => void;
  // Empty-state guidance (no session selected).
  projects: SidebarProject[];
  selectedProject: string | null;
  hasModel: boolean;
  pinnedProjectIds: string[];
  togglePinProject: (projectId: string) => void;
  onSwitchProject: (projectId: string) => void;
  onNewProject: () => void;
  onOpenSettings: () => void;
}

export function ChatWindow({
  history, run, branches, composer, actions, error, clearError,
  projects, selectedProject, hasModel, pinnedProjectIds, togglePinProject,
  onSwitchProject, onNewProject, onOpenSettings,
}: ChatWindowProps) {
  const messagesRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const preserveScroll = useRef(false);
  const previousMessageCount = useRef(0);
  // Hero project chip + composer share one dropdown control; both live in the
  // middle channel so the sidebar keeps its own independent selector.
  const heroProject = useProjectSelector();
  const waiting = run.activeRunStatus === "waiting_for_approval" ||
    run.activeRunStatus === "waiting_delegation_admission" || Boolean(run.pendingApproval);
  const reconnecting = run.status === "Run connection interrupted" && !waiting;

  // Requesting a project from the empty state: with no projects yet the chip
  // and the inert composer go straight to the create dialog (dsh add-only);
  // otherwise they toggle the shared menu anchored under the hero chip.
  const requestProject = useCallback(() => {
    if (projects.length === 0) {
      onNewProject();
      return;
    }
    if (heroProject.open) heroProject.close();
    else heroProject.openDropdown();
  }, [heroProject, onNewProject, projects.length]);

  useEffect(() => {
    const container = messagesRef.current;
    if (!container || preserveScroll.current) return;
    const nearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 120;
    if (previousMessageCount.current === 0 || nearBottom) {
      bottomRef.current?.scrollIntoView({ behavior: previousMessageCount.current === 0 ? "auto" : "smooth" });
    }
    previousMessageCount.current = history.messages.length;
  }, [history.messages, run.status]);

  useEffect(() => { previousMessageCount.current = 0; }, [history.activeBranchId, history.sessionId]);

  async function loadOlder() {
    const container = messagesRef.current;
    if (!container) return;
    const previousHeight = container.scrollHeight;
    const previousTop = container.scrollTop;
    preserveScroll.current = true;
    const loaded = await history.loadOlder();
    if (loaded) {
      requestAnimationFrame(() => requestAnimationFrame(() => {
        container.scrollTop = previousTop + (container.scrollHeight - previousHeight);
        preserveScroll.current = false;
        previousMessageCount.current = history.messages.length;
      }));
    } else preserveScroll.current = false;
  }

  const isBlank = Boolean(history.sessionId)
    && !history.activeLeafMessageId
    && history.messages.length === 0
    && !run.activeRun
    && !run.pendingApproval;
  const isHero = !history.sessionId || isBlank;
  const inert = !selectedProject;

  return <main className="chat-area" data-phase={isHero ? "hero" : "active"}>
    {error && <div className="error-bar" role="alert"><span>{error}</span>
      <button onClick={clearError} aria-label="Dismiss error"><X size={15} aria-hidden="true" /></button></div>}
    {run.recovery && <div className={`recovery-bar ${run.recovery.retryable ? "is-retryable" : "is-blocked"}`} data-testid="run-recovery">
      <span><strong>{run.recovery.run.status === "interrupted" ? "Run interrupted" : "Run failed"}</strong>
        {run.recovery.retryable ? `Attempt ${run.recovery.run.attempt} can be retried safely.` : recoveryMessage(run.recovery.blockedReason)}</span>
      {run.recovery.retryable && <button type="button" onClick={run.retryRun} disabled={run.retrying || Boolean(run.activeRun)}>
        {run.retrying ? "Retrying…" : "Retry"}
      </button>}
    </div>}
    {!isHero && <div className="messages" ref={messagesRef} data-testid="chat-messages">
      <div className="conversation-viewport">
        {history.hasMore && <div className="history-control">
          <button type="button" onClick={loadOlder} disabled={history.loadingOlder}>
            {history.loadingOlder ? "Loading earlier messages…" : "Load earlier messages"}
          </button>
        </div>}
        {history.loading && <div className="history-state">Loading conversation…</div>}
        {history.error && <div className="history-state history-state-error">Conversation history unavailable: {history.error}</div>}
        {!history.loading && history.messages.length === 0 && !history.error && !run.pendingApproval && <div className="history-empty">
          <strong>New conversation</strong><span>Start with a question, file, or analysis task.</span>
        </div>}
        <ConversationTimeline sessionId={history.sessionId ?? ""} nodes={history.messages} pendingApproval={run.pendingApproval} resolvingApproval={run.resolvingApproval}
          decideApproval={run.decideApproval} activeLeafMessageId={history.activeLeafMessageId}
          branchDisabled={Boolean(run.activeRun) || branches.changing} createBranch={branches.createBranch}
          runStatus={run.activeRun || run.status ? {
            status: run.status, active: Boolean(run.activeRun), waiting, reconnecting, compacting: run.compacting,
            permissionMode: composer.displayedPermissionMode,
          } : undefined} />
        <StreamingStatusBar
          status={run.activeRun ? run.status : ""}
          activeRun={Boolean(run.activeRun)}
          waiting={waiting}
          reconnecting={reconnecting}
          compacting={run.compacting}
          cacheReadTokens={run.usage?.cacheReadTokens ?? 0}
          inputTokens={(run.usage?.uncachedInputTokens ?? 0) + (run.usage?.cacheReadTokens ?? 0) + (run.usage?.cacheWriteTokens ?? 0)}
          outputTokens={run.usage?.outputTokens ?? 0}
        />
        <div ref={bottomRef} />
      </div>
    </div>}
    {!isHero && <BackgroundDelegationStrip sessionId={history.sessionId ?? undefined} />}
    {!isHero && composer.compaction.open && (
      <CompactionPromptBar
        value={composer.compaction.instructions}
        onChange={composer.compaction.setInstructions}
        busy={composer.compaction.busy}
        onConfirm={composer.compaction.confirm}
        onCancel={composer.compaction.cancel}
      />
    )}
    <div className="composer-seat">
      <div className={isHero ? "composer-stack composer-hero" : "composer-stack"}>
        {isHero && <HeroGlow />}
        {isHero && (
          <EmptyHero
            hasModel={hasModel}
            onOpenSettings={onOpenSettings}
          />
        )}
        {isHero && (
          <div className="hero-workspace-row">
            <HeroProjectChip
              projects={projects}
              selectedProject={selectedProject}
              pinnedProjectIds={pinnedProjectIds}
              togglePinProject={togglePinProject}
              onRequestProject={requestProject}
              onSwitchProject={onSwitchProject}
              projectSelector={heroProject}
            />
          </div>
        )}
        <Composer selectedSession={history.sessionId} activeLeafMessageId={history.activeLeafMessageId} input={composer.input} setInput={composer.setInput}
          activeRun={Boolean(run.activeRun)} compacting={run.compacting} hasPendingImage={Boolean(composer.pendingImage)} reconnecting={reconnecting}
          inert={inert} hero={isHero}
          onRequestProject={requestProject}
          permissionMode={composer.displayedPermissionMode} permissionReady={composer.permissionReady} setPermissionMode={composer.setPermissionMode}
          models={composer.models} providers={composer.providers} selectedModelId={composer.selectedModelId} setSelectedModelId={composer.setSelectedModelId}
          thinkingEffort={composer.thinkingEffort} setThinkingEffort={composer.setThinkingEffort}
          roles={composer.roles} selectedRoleId={composer.selectedRoleId} setSelectedRoleId={composer.setSelectedRoleId}
          textAttachments={composer.textAttachments} removeTextAttachment={composer.removeTextAttachment}
          pendingImage={composer.pendingImage} clearPendingImage={composer.clearPendingImage}
          attachFiles={composer.attachFiles} uploadImage={composer.uploadImage} submit={actions.submit} steer={actions.steer} followUp={actions.followUp} cancel={run.cancel} compactSession={composer.compactSession}
          pendingFollowUps={run.pendingFollowUps}
          promptTemplates={composer.promptPanel.templates} showPromptPanel={composer.promptPanel.show} onPromptSelect={composer.promptPanel.onSelect}
          panelRoles={composer.promptPanel.roles} panelFlows={composer.promptPanel.flows} onRoleSelect={composer.promptPanel.onRoleSelect} onFlowSelect={composer.promptPanel.onFlowSelect}
          onPromptPanelClose={composer.promptPanel.onClose}
          expanding={composer.promptPanel.expanding} expandDiag={composer.promptPanel.expandDiag}
          contextUsage={run.contextUsage} stats={run.stats} />
      </div>
    </div>
  </main>;
}

function recoveryMessage(reason?: RunRecovery["blockedReason"]): string {
  switch (reason) {
    case "side_effect_boundary": return "Retry is unavailable because tools may have changed external state.";
    case "projected_output": return "Retry is unavailable because this attempt already committed output.";
    case "active_run": return "Another run is active.";
    default: return "This attempt is no longer on the active branch.";
  }
}
