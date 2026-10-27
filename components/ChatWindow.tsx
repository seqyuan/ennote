"use client";

import { ArrowDown, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { ChatRail } from "@/components/ChatRail";
import { SelectionActions } from "@/components/SelectionActions";
import { Composer } from "@/components/Composer";
import { EmptyHero, HeroGlow } from "@/components/EmptyHero";
import { HeroProjectChip } from "@/components/HeroProjectChip";
import { BackgroundDelegationStrip } from "@/components/BackgroundDelegationStrip";
import { CompactionPromptBar } from "@/components/CompactionPromptBar";
import { ConversationTimeline } from "@/components/ConversationTimeline";
import { useT } from "@/components/LocaleProvider";
import { StreamingStatusBar } from "@/components/StreamingStatusBar";
import type { BranchView, ChatActions, ComposerView, HistoryView, RunView } from "@/hooks/useChatController";
import type { components } from "@/lib/worker-api.gen";
import { useProjectSelector } from "@/hooks/useProjectSelector";
import { useTextSelection } from "@/hooks/useTextSelection";
import { buildExplainRequest, buildQuote } from "@/lib/selection-quote";
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
  const t = useT();
  const messagesRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const preserveScroll = useRef(false);
  const previousMessageCount = useRef(0);
  const newMessageCount = useRef(0);
  const [atBottom, setAtBottom] = useState(true);
  const [hasNew, setHasNew] = useState(false);
  const [focusNonce, setFocusNonce] = useState(0);
  // Hero project chip + composer share one dropdown control; both live in the
  // middle channel so the sidebar keeps its own independent selector.
  const heroProject = useProjectSelector();
  const waiting = run.activeRunStatus === "waiting_for_approval" ||
    run.activeRunStatus === "waiting_delegation_admission" || Boolean(run.pendingApproval);
  const waitingForQuestion = run.pendingApproval?.kind === "question";
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

  useEffect(() => {
    previousMessageCount.current = 0;
    newMessageCount.current = 0;
    queueMicrotask(() => {
      setAtBottom(true);
      setHasNew(false);
    });
  }, [history.activeBranchId, history.sessionId]);

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
  const textSelection = useTextSelection(messagesRef, { enabled: !isHero });
  const { clear: clearSelection } = textSelection;

  const quoteSelection = useCallback((text: string) => {
    const quote = buildQuote(text, t("selection.source.chat"));
    if (!quote) return;
    composer.setInput([composer.input, quote].filter(Boolean).join("\n\n"));
    clearSelection();
    setFocusNonce((value) => value + 1);
  }, [clearSelection, composer, t]);

  const explainSelection = useCallback((text: string) => {
    const quote = buildQuote(text, t("selection.source.chat"));
    if (!quote) return;
    composer.setInput([composer.input, buildExplainRequest(t("selection.explainPrompt"), quote)].filter(Boolean).join("\n\n"));
    clearSelection();
    setFocusNonce((value) => value + 1);
  }, [clearSelection, composer, t]);

  // Follow the reader: the rail and jump-to-latest affordance only matter when
  // the transcript is actually scrollable away from its floor.
  useEffect(() => {
    const container = messagesRef.current;
    if (!container) return;
    const onScroll = () => {
      const bottom = container.scrollHeight - container.scrollTop - container.clientHeight < 80;
      setAtBottom(bottom);
      if (bottom) setHasNew(false);
    };
    container.addEventListener("scroll", onScroll, { passive: true });
    onScroll();
    return () => container.removeEventListener("scroll", onScroll);
  }, [history.sessionId, isHero]);

  useEffect(() => {
    const count = history.messages.length;
    if (count > newMessageCount.current && newMessageCount.current > 0 && !atBottom) {
      setHasNew(true);
    }
    newMessageCount.current = count;
  }, [history.messages.length, atBottom]);

  const jumpToBottom = useCallback(() => {
    const container = messagesRef.current;
    if (!container) return;
    container.scrollTo({ top: container.scrollHeight, behavior: "smooth" });
    setHasNew(false);
  }, []);

  return <main className="chat-area" data-phase={isHero ? "hero" : "active"}>
    {error && <div className="error-bar" role="alert"><span>{error}</span>
      <button onClick={clearError} aria-label={t("conversation.chat.dismissError")}><X size={15} aria-hidden="true" /></button></div>}
    {run.recovery && <div className={`recovery-bar ${run.recovery.retryable ? "is-retryable" : "is-blocked"}`} data-testid="run-recovery">
      <span><strong>{run.recovery.run.status === "interrupted" ? t("conversation.chat.runInterrupted") : t("conversation.chat.runFailed")}</strong>
        {run.recovery.retryable
          ? t("conversation.chat.attemptRetryable").replace("{attempt}", String(run.recovery.run.attempt))
          : recoveryMessage(run.recovery.blockedReason, t)}</span>
      {run.recovery.retryable && <button type="button" onClick={run.retryRun} disabled={run.retrying || Boolean(run.activeRun)}>
        {run.retrying ? t("conversation.chat.retrying") : t("conversation.chat.retry")}
      </button>}
    </div>}
    {!isHero && <div className="chat-scroll-region">
    <div className="messages" ref={messagesRef} data-testid="chat-messages">
      <div className="conversation-viewport">
        {history.hasMore && <div className="history-control">
          <button type="button" onClick={loadOlder} disabled={history.loadingOlder}>
            {history.loadingOlder ? t("conversation.chat.loadingEarlier") : t("conversation.chat.loadEarlier")}
          </button>
        </div>}
        {history.loading && <div className="history-state">{t("conversation.chat.loading")}</div>}
        {history.error && <div className="history-state history-state-error">{t("conversation.chat.historyUnavailable").replace("{message}", history.error)}</div>}
        {!history.loading && history.messages.length === 0 && !history.error && !run.pendingApproval && <div className="history-empty">
          <strong>{t("conversation.chat.newConversation")}</strong><span>{t("conversation.chat.startHint")}</span>
        </div>}
        <ConversationTimeline sessionId={history.sessionId ?? ""} nodes={history.messages} pendingApproval={run.pendingApproval} resolvingApproval={run.resolvingApproval}
          decideApproval={run.decideApproval}
          answeringQuestion={run.answeringQuestion} answerQuestion={run.answerQuestion}
          activeLeafMessageId={history.activeLeafMessageId}
          branchDisabled={Boolean(run.activeRun) || branches.changing} createBranch={branches.createBranch}
          runStatus={run.activeRun || run.status ? {
            status: run.status, active: Boolean(run.activeRun), waiting, waitingForQuestion, reconnecting, compacting: run.compacting,
            permissionMode: composer.displayedPermissionMode,
          } : undefined} />
        <StreamingStatusBar
          status={run.activeRun ? run.status : ""}
          activeRun={Boolean(run.activeRun)}
          waiting={waiting}
          waitingForQuestion={waitingForQuestion}
          reconnecting={reconnecting}
          compacting={run.compacting}
          cacheReadTokens={run.usage?.cacheReadTokens ?? 0}
          inputTokens={(run.usage?.uncachedInputTokens ?? 0) + (run.usage?.cacheReadTokens ?? 0) + (run.usage?.cacheWriteTokens ?? 0)}
          outputTokens={run.usage?.outputTokens ?? 0}
        />
        <div ref={bottomRef} />
      </div>
    </div>
    <ChatRail nodes={history.messages} scrollContainer={messagesRef} />
    {!atBottom && (
      <button
        type="button"
        className="chat-jump-latest"
        data-new={hasNew || undefined}
        onClick={jumpToBottom}
        aria-label={t(hasNew ? "conversation.newMessages" : "conversation.jumpLatest")}
      >
        <ArrowDown size={14} aria-hidden="true" />
        {t(hasNew ? "conversation.newMessages" : "conversation.jumpLatest")}
      </button>
    )}
    </div>}
    {!isHero && textSelection.selection && (
      <SelectionActions
        selection={textSelection.selection}
        onQuote={quoteSelection}
        onExplain={explainSelection}
        onDismiss={clearSelection}
      />
    )}
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
          focusNonce={focusNonce}
          activeRun={Boolean(run.activeRun)} questionPending={Boolean(waitingForQuestion)} compacting={run.compacting} hasPendingImage={Boolean(composer.pendingImage)} reconnecting={reconnecting}
          inert={inert} hero={isHero}
          onRequestProject={requestProject}
          permissionMode={composer.displayedPermissionMode} permissionReady={composer.permissionReady} setPermissionMode={composer.setPermissionMode}
          models={composer.models} providers={composer.providers} selectedModelId={composer.selectedModelId} setSelectedModelId={composer.setSelectedModelId}
          thinkingEffort={composer.thinkingEffort} setThinkingEffort={composer.setThinkingEffort}
          roles={composer.roles} selectedRoleId={composer.selectedRoleId} setSelectedRoleId={composer.setSelectedRoleId}
          textAttachments={composer.textAttachments} removeTextAttachment={composer.removeTextAttachment}
          pendingImage={composer.pendingImage} clearPendingImage={composer.clearPendingImage}
          attachFiles={composer.attachFiles} uploadImage={composer.uploadImage} submit={actions.submit} steer={actions.steer} steerText={actions.steerText} followUp={actions.followUp} cancel={run.cancel} cancelFollowUp={run.cancelFollowUp} compactSession={composer.compactSession}
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

function recoveryMessage(reason: RunRecovery["blockedReason"] | undefined, t: (key: string) => string): string {
  switch (reason) {
    case "side_effect_boundary": return t("conversation.chat.recovery.sideEffect");
    case "projected_output": return t("conversation.chat.recovery.projectedOutput");
    case "active_run": return t("conversation.chat.recovery.activeRun");
    default: return t("conversation.chat.recovery.notActive");
  }
}
