import type { PendingImage, TextAttachment } from "@/components/Composer";
import type { ModelProfile, RoleSummary } from "@/components/settings/types";
import type { ApprovalDecision, ToolApprovalRequest } from "@/lib/approval";
import type { ConversationNode } from "@/lib/chat-messages";
import type { PermissionMode, ThinkingEffort } from "@/lib/permission-mode";
import type { components } from "@/lib/worker-api.gen";

type SessionBranch = components["schemas"]["SessionBranch"];
type RunRecovery = components["schemas"]["RunRecovery"];
export type SessionContextUsage = components["schemas"]["SessionContextUsage"];
export type SessionStats = components["schemas"]["SessionStats"];

export type HistoryView = {
  sessionId: string | null;
  activeBranchId?: string;
  activeLeafMessageId?: string;
  messages: ConversationNode[];
  loading: boolean;
  loadingOlder: boolean;
  error: string | null;
  hasMore: boolean;
  loadOlder: () => Promise<boolean>;
};

export type RunUsage = {
  uncachedInputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  outputTokens: number;
  reasoningTokens: number;
};

export type RunView = {
  activeRun: string | null;
  activeRunStatus?: string;
  status: string;
  usage: RunUsage | null;
  contextUsage: SessionContextUsage | null;
  stats: SessionStats | null;
  compacting: boolean;
  pendingApproval: ToolApprovalRequest | null;
  resolvingApproval: ApprovalDecision | null;
  decideApproval: (decision: ApprovalDecision) => void;
  cancel: () => void;
  pendingFollowUps: { id: string; text: string }[];
  recovery: RunRecovery | null;
  retrying: boolean;
  retryRun: () => void;
  error: string | null;
  setError: (error: string | null) => void;
  clearError: () => void;
};

export type BranchView = {
  branches: SessionBranch[];
  activeBranchId?: string;
  loading: boolean;
  changing: boolean;
  error: string | null;
  createBranch: (messageId: string) => void;
  activateBranch: (branchId: string) => void;
};

export type ComposerView = {
  sessionId: string | null;
  activeLeafMessageId?: string;
  input: string;
  setInput: (value: string) => void;
  pendingImage: PendingImage | null;
  clearPendingImage: () => void;
  uploadImage: (file: File) => void;
  textAttachments: TextAttachment[];
  removeTextAttachment: (id: string) => void;
  attachFiles: (files: File[]) => void;
  models: ModelProfile[];
  selectedModelId: string | null;
  setSelectedModelId: (modelId: string) => void;
  thinkingEffort: ThinkingEffort;
  setThinkingEffort: (effort: ThinkingEffort) => void;
  roles: RoleSummary[];
  selectedRoleId: string | null;
  setSelectedRoleId: (roleId: string | null) => void;
  permissionMode: PermissionMode;
  displayedPermissionMode: PermissionMode;
  permissionReady: boolean;
  setPermissionMode: (mode: PermissionMode) => void;
  compactSession: () => void;
  compaction: {
    open: boolean;
    instructions: string;
    busy: boolean;
    setInstructions: (value: string) => void;
    confirm: () => void;
    cancel: () => void;
  };
  promptPanel: {
    templates: { name: string; description: string; argumentHint: string; source: string; editable: boolean }[];
    roles: { id: string; handle: string; name: string; description?: string }[];
    flows: { name: string; version?: number }[];
    show: boolean;
    onSelect: (name: string) => void;
    onRoleSelect: (roleId: string, handle: string) => void;
    onFlowSelect: (name: string, version?: number) => void;
    onClose: () => void;
    expanding: boolean;
    expandDiag: string | null;
  };
};

export type ChatActions = {
  submit: () => void;
  steer: () => void;
  followUp: () => void;
};

export type ChatController = {
  history: HistoryView;
  run: RunView;
  branches: BranchView;
  composer: ComposerView;
  actions: ChatActions;
  error: string | null;
  clearError: () => void;
};
