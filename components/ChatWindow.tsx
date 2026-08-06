"use client";

import { X } from "lucide-react";
import { useEffect, useRef } from "react";
import { Composer, type TextAttachment } from "@/components/Composer";
import { BackgroundDelegationStrip } from "@/components/BackgroundDelegationStrip";
import { FlowCheckApprovalStrip } from "@/components/FlowCheckApprovalStrip";
import { CompactionPromptBar } from "@/components/CompactionPromptBar";
import { ConversationTimeline } from "@/components/ConversationTimeline";
import { StreamingStatusBar } from "@/components/StreamingStatusBar";
import type { ModelProfile, RoleSummary } from "@/components/settings/types";
import type { ApprovalDecision, ToolApprovalRequest } from "@/lib/approval";
import type { ConversationNode } from "@/lib/chat-messages";
import type { PermissionMode } from "@/lib/permission-mode";
import type { components } from "@/lib/worker-api.gen";

type RunRecovery = components["schemas"]["RunRecovery"];

export interface PendingImage {
  id: string;
  name: string;
  mimeType: string;
  width?: number;
  height?: number;
}

interface ChatWindowProps {
  projectId: string | null;
  selectedSession: string | null;
  activeLeafMessageId?: string;
  activeBranchId?: string;
  branchChanging: boolean;
  createBranch: (messageId: string) => void;
  recovery: RunRecovery | null;
  retrying: boolean;
  retryRun: () => void;
  messages: ConversationNode[];
  historyLoading: boolean;
  historyLoadingOlder: boolean;
  historyError: string | null;
  hasMoreHistory: boolean;
  loadOlderHistory: () => Promise<boolean>;
  error: string | null;
  clearError: () => void;
  status: string;
  input: string;
  setInput: (value: string) => void;
  activeRun: string | null;
  activeRunStatus?: string;
  compacting: boolean;
  permissionMode: PermissionMode;
  permissionReady: boolean;
  setPermissionMode: (mode: PermissionMode) => void;
  pendingApproval: ToolApprovalRequest | null;
  resolvingApproval: ApprovalDecision | null;
  decideApproval: (decision: ApprovalDecision) => void;
  pendingImage: PendingImage | null;
  clearPendingImage: () => void;
  uploadImage: (file: File) => void;
  models: ModelProfile[];
  selectedModelId: string | null;
  setSelectedModelId: (modelId: string) => void;
  roles: RoleSummary[];
  selectedRoleId: string | null;
  setSelectedRoleId: (roleId: string | null) => void;
  textAttachments: TextAttachment[];
  removeTextAttachment: (id: string) => void;
  attachFiles: (files: File[]) => void;
  submit: () => void;
  steer: () => void;
  cancel: () => void;
  compactSession: () => void;
  compactionPromptOpen: boolean;
  compactionInstructions: string;
  compactionBusy: boolean;
  setCompactionInstructions: (value: string) => void;
  confirmCompaction: () => void;
  cancelCompaction: () => void;
  // Prompt templates.
  promptTemplates: { name: string; description: string; argumentHint: string; source: string; editable: boolean }[];
  showPromptPanel: boolean;
  onPromptSelect: (name: string) => void;
  onPromptPanelClose: () => void;
  expanding: boolean;
  expandDiag: string | null;
}

export function ChatWindow({
  projectId, selectedSession, activeLeafMessageId, activeBranchId, branchChanging,
  createBranch, recovery, retrying, retryRun, messages, historyLoading, historyLoadingOlder, historyError,
  hasMoreHistory, loadOlderHistory, error, clearError, status, input, setInput, activeRun, activeRunStatus, compacting,
  permissionMode, permissionReady, setPermissionMode, pendingApproval, resolvingApproval, decideApproval,
  pendingImage, clearPendingImage, uploadImage, models, selectedModelId, setSelectedModelId,
  roles, selectedRoleId, setSelectedRoleId, textAttachments, removeTextAttachment, attachFiles, submit, steer, cancel, compactSession,
  compactionPromptOpen, compactionInstructions, compactionBusy, setCompactionInstructions, confirmCompaction, cancelCompaction,
  promptTemplates, showPromptPanel, onPromptSelect, onPromptPanelClose, expanding, expandDiag,
}: ChatWindowProps) {
  const messagesRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const preserveScroll = useRef(false);
  const previousMessageCount = useRef(0);
  const waiting = activeRunStatus === "waiting_for_approval" ||
    activeRunStatus === "waiting_delegation_admission" || Boolean(pendingApproval);
  const reconnecting = status === "Run connection interrupted" && !waiting;

  useEffect(() => {
    const container = messagesRef.current;
    if (!container || preserveScroll.current) return;
    const nearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 120;
    if (previousMessageCount.current === 0 || nearBottom) {
      bottomRef.current?.scrollIntoView({ behavior: previousMessageCount.current === 0 ? "auto" : "smooth" });
    }
    previousMessageCount.current = messages.length;
  }, [messages, status]);

  useEffect(() => { previousMessageCount.current = 0; }, [activeBranchId, selectedSession]);

  async function loadOlder() {
    const container = messagesRef.current;
    if (!container) return;
    const previousHeight = container.scrollHeight;
    const previousTop = container.scrollTop;
    preserveScroll.current = true;
    const loaded = await loadOlderHistory();
    if (loaded) {
      requestAnimationFrame(() => requestAnimationFrame(() => {
        container.scrollTop = previousTop + (container.scrollHeight - previousHeight);
        preserveScroll.current = false;
        previousMessageCount.current = messages.length;
      }));
    } else preserveScroll.current = false;
  }

  return <main className="chat-area">
    {error && <div className="error-bar" role="alert"><span>{error}</span>
      <button onClick={clearError} aria-label="Dismiss error"><X size={15} aria-hidden="true" /></button></div>}
    {recovery && <div className={`recovery-bar ${recovery.retryable ? "is-retryable" : "is-blocked"}`} data-testid="run-recovery">
      <span><strong>{recovery.run.status === "interrupted" ? "Run interrupted" : "Run failed"}</strong>
        {recovery.retryable ? `Attempt ${recovery.run.attempt} can be retried safely.` : recoveryMessage(recovery.blockedReason)}</span>
      {recovery.retryable && <button type="button" onClick={retryRun} disabled={retrying || Boolean(activeRun)}>
        {retrying ? "Retrying…" : "Retry"}
      </button>}
    </div>}
    <div className="messages" ref={messagesRef} data-testid="chat-messages">
      <div className="conversation-viewport">
        {selectedSession && hasMoreHistory && <div className="history-control">
          <button type="button" onClick={loadOlder} disabled={historyLoadingOlder}>
            {historyLoadingOlder ? "Loading earlier messages…" : "Load earlier messages"}
          </button>
        </div>}
        {historyLoading && <div className="history-state">Loading conversation…</div>}
        {historyError && <div className="history-state history-state-error">Conversation history unavailable: {historyError}</div>}
        {!historyLoading && selectedSession && messages.length === 0 && !historyError && !pendingApproval && <div className="history-empty">
          <strong>New conversation</strong><span>Start with a question, file, or analysis task.</span>
        </div>}
        {!selectedSession && <div className="history-empty"><strong>No session selected</strong><span>Choose a project and session to continue.</span></div>}
        <ConversationTimeline sessionId={selectedSession ?? ""} nodes={messages} pendingApproval={pendingApproval} resolvingApproval={resolvingApproval}
          decideApproval={decideApproval} activeLeafMessageId={activeLeafMessageId}
          branchDisabled={Boolean(activeRun) || branchChanging} createBranch={createBranch}
          runStatus={activeRun || status ? {
            status, active: Boolean(activeRun), waiting, reconnecting, compacting, permissionMode,
          } : undefined} />
        <StreamingStatusBar
          status={activeRun ? status : ""}
          activeRun={Boolean(activeRun)}
          waiting={waiting}
          reconnecting={reconnecting}
          compacting={compacting}
        />
        <div ref={bottomRef} />
      </div>
    </div>
    {pendingImage && <div className="attachment-strip">
      <span>{pendingImage.name} · {pendingImage.width}×{pendingImage.height}</span>
      <button onClick={clearPendingImage} aria-label="Remove image"><X size={14} aria-hidden="true" /></button>
    </div>}
    <BackgroundDelegationStrip sessionId={selectedSession ?? undefined} />
    <FlowCheckApprovalStrip projectId={projectId} sessionId={selectedSession ?? undefined} />
    {compactionPromptOpen && (
      <CompactionPromptBar
        value={compactionInstructions}
        onChange={setCompactionInstructions}
        busy={compactionBusy}
        onConfirm={confirmCompaction}
        onCancel={cancelCompaction}
      />
    )}
    <Composer selectedSession={selectedSession} activeLeafMessageId={activeLeafMessageId} input={input} setInput={setInput}
      activeRun={Boolean(activeRun)} compacting={compacting} hasPendingImage={Boolean(pendingImage)} reconnecting={reconnecting}
      permissionMode={permissionMode} permissionReady={permissionReady} setPermissionMode={setPermissionMode}
      models={models} selectedModelId={selectedModelId} setSelectedModelId={setSelectedModelId}
      roles={roles} selectedRoleId={selectedRoleId} setSelectedRoleId={setSelectedRoleId}
      textAttachments={textAttachments} removeTextAttachment={removeTextAttachment} attachFiles={attachFiles}
      uploadImage={uploadImage} submit={submit} steer={steer} cancel={cancel} compactSession={compactSession}
      promptTemplates={promptTemplates} showPromptPanel={showPromptPanel} onPromptSelect={onPromptSelect}
      onPromptPanelClose={onPromptPanelClose}
      expanding={expanding} expandDiag={expandDiag} />
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
