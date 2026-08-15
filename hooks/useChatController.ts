"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { TextAttachment, PendingImage } from "@/components/Composer";
import type { ModelProfile, RoleSummary, RunAgentFlow, Session } from "@/components/settings/types";
import type { AgentRun, ApprovalDecision, ToolApprovalRequest } from "@/lib/approval";
import type { ConversationNode } from "@/lib/chat-messages";
import {
  permissionModeForPolicyID,
  permissionPolicyID,
  withRunConfig,
  type PermissionMode,
  type ThinkingEffort,
} from "@/lib/permission-mode";
import { errorMessage } from "@/lib/provider-errors";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";
import { useAgentSession } from "@/hooks/useAgentSession";
import { useSessionMessages } from "@/hooks/useSessionMessages";
import { useRunRecovery } from "@/hooks/useRunRecovery";
import { useSessionBranches } from "@/hooks/useSessionBranches";
import type { usePromptTemplates } from "@/hooks/usePromptTemplates";
import type { useSettingsProfiles } from "@/hooks/useSettingsProfiles";

type TurnSubmission = components["schemas"]["TurnSubmission"];
type ImageArtifact = components["schemas"]["ImageArtifact"];
type CompactionSubmission = components["schemas"]["CompactionSubmission"];
type GlobalRoleSummary = components["schemas"]["GlobalRoleSummary"];
type GlobalRoleDetail = components["schemas"]["GlobalRoleDetail"];
type FileRevision = { version: number; publishedAt: string };
type SessionBranch = components["schemas"]["SessionBranch"];
type RunRecovery = components["schemas"]["RunRecovery"];

function genId(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}

const TEXT_ATTACHMENT_EXTENSIONS = new Set([
  "txt", "md", "mdx", "json", "yaml", "yml", "toml", "csv", "tsv", "log", "js", "jsx", "ts", "tsx",
  "py", "r", "go", "rs", "java", "c", "cpp", "h", "hpp", "css", "html", "xml", "sh", "bash", "sql",
]);

function isSupportedTextAttachment(file: File): boolean {
  if (file.type.startsWith("text/")) return true;
  const extension = file.name.toLowerCase().split(".").pop() ?? "";
  return TEXT_ATTACHMENT_EXTENSIONS.has(extension) || ["dockerfile", "makefile"].includes(file.name.toLowerCase());
}

function appendTextAttachments(text: string, attachments: TextAttachment[]): string {
  if (attachments.length === 0) return text;
  const sections = attachments.map((attachment) => [
    `--- Attached file: ${attachment.name} ---`,
    attachment.text,
    `--- End attached file: ${attachment.name} ---`,
  ].join("\n"));
  return [text.trim(), ...sections].filter(Boolean).join("\n\n");
}

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

export type RunView = {
  activeRun: string | null;
  activeRunStatus?: string;
  status: string;
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

export function useChatController(deps: ChatControllerDeps): ChatController {
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
  });
  const recoveryData = useRunRecovery(selectedSession, activeBranchId, agent.activeRunID);
  const branchesData = useSessionBranches({ sessionId: selectedSession, activeBranchId, onSessionUpdated: updateSession });

  // Composer state (owned here; reset declaratively on session switch).
  const [input, setInput] = useState("");
  const [pendingImage, setPendingImage] = useState<ImageArtifact | null>(null);
  const [textAttachments, setTextAttachments] = useState<TextAttachment[]>([]);
  const [modelOverrides, setModelOverrides] = useState<Record<string, string>>({});
  const [permissionMode, setPermissionMode] = useState<PermissionMode>("discuss");
  const [thinkingEffort, setThinkingEffort] = useState<ThinkingEffort>("default");
  const [compactionPrompt, setCompactionPrompt] = useState<{ open: boolean; instructions: string; busy: boolean }>({
    open: false, instructions: "", busy: false,
  });
  const [selectedRoleId, setSelectedRoleId] = useState<string | null>(null);
  const [roles, setRoles] = useState<RoleSummary[]>([]);
  const selectedSessionRef = useRef<string | null>(selectedSession);

  // Prompt template expansion state.
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
  }, []);

  useEffect(() => {
    selectedSessionRef.current = selectedSession;
  }, [selectedSession]);

  // Declarative composer reset on session switch (paint-before, no flash of a
  // previous session's draft). Post-send clearing stays explicit in sendTurn.
  // The reset intentionally mirrors what selectSession used to do synchronously;
  // moving it here is the point of the design (§4.1.2), so the cascading-state
  // rule is bypassed for this deliberate effect.
  useLayoutEffect(() => {
    // Deliberate declarative reset (§4.1.2): mirrors the synchronous clearing
    // selectSession used to do; cascading-state rule bypassed intentionally.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setInputVersioned("");
    setPendingImage(null);
    setTextAttachments([]);
  }, [selectedSession, setInputVersioned]);

  // Role selection does not survive a project switch (declarative, §4.1.3).
  useLayoutEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSelectedRoleId(null);
  }, [selectedProject]);

  // Role catalog: global roles with a /v1/roles fallback.
  useEffect(() => {
    if (!selectedProject) return;
    let cancelled = false;
    void apiFetch<GlobalRoleSummary[]>("/v1/global-roles")
      .then(async (catalog) => {
        const resolved = await Promise.all(catalog.filter((entry) => !entry.error).map(async (entry): Promise<RoleSummary | null> => {
          try {
            const [detail, revisions] = await Promise.all([
              apiFetch<GlobalRoleDetail>(`/v1/global-roles/${encodeURIComponent(entry.id)}`),
              apiFetch<FileRevision[]>(`/v1/global-roles/${encodeURIComponent(entry.id)}/versions`),
            ]);
            const latest = revisions.at(-1);
            if (!latest) return null;
            return {
              id: entry.id, handle: detail.document.handle, name: detail.document.name,
              description: detail.document.description, positioning: detail.document.positioning,
              icon: detail.document.icon, color: detail.document.color, scope: "global", status: "active",
              sourceKind: "managed", sourceLocator: detail.path,
              currentVersionId: `v${String(latest.version).padStart(6, "0")}`,
              currentVersion: latest.version, updatedAt: latest.publishedAt,
            };
          } catch {
            return null;
          }
        }));
        if (cancelled) return;
        const published = resolved.filter((role): role is RoleSummary => role !== null);
        setRoles(published);
        setSelectedRoleId((current) => published.some((role) => role.id === current) ? current : null);
      })
      .catch(async () => {
        // SQL-backed API test adapters expose the managed Role catalog.
        try {
          const params = new URLSearchParams({ projectId: selectedProject, status: "active", limit: "100" });
          const page = await apiFetch<{ items: RoleSummary[] }>(`/v1/roles?${params}`);
          if (cancelled) return;
          const published = page.items.filter((role) => Boolean(role.currentVersionId));
          setRoles(published);
          setSelectedRoleId((current) => published.some((role) => role.id === current) ? current : null);
        } catch {
          if (!cancelled) setRoles([]);
        }
      });
    return () => { cancelled = true; };
  }, [selectedProject]);

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

  // Derived composer inputs.
  const selectedModelId = selectedSession
    ? modelOverrides[selectedSession]
      ?? sessionRecord?.defaultModelProfileId
      ?? settings.models.find((model) => model.isDefault)?.id
      ?? settings.models[0]?.id
      ?? null
    : null;

  const selectedPermissionPolicyID = useCallback(
    () => { return permissionPolicyID(settings.policies, permissionMode); },
    [permissionMode, settings.policies],
  );
  const displayedPermissionMode = useCallback(() => {
    const requested = agent.activeRun?.requestedConfig as Record<string, unknown> | undefined;
    return permissionModeForPolicyID(settings.policies, requested?.toolPolicyProfileId) ?? permissionMode;
  }, [agent.activeRun, permissionMode, settings.policies]);

  // ——— Chat actions ———

  const sendTurn = useCallback(async (text: string, toolPolicyProfileId: string) => {
    if (!selectedSession) return;
    const sessionAtSend = selectedSession;
    const image = pendingImage;
    const attachments = textAttachments;
    const contextualText = appendTextAttachments(text, attachments);
    const attachmentSummary = attachments.length ? `[Files: ${attachments.map((item) => item.name).join(", ")}]` : "";
    setInputVersioned("");
    setPendingImage(null);
    setTextAttachments([]);
    agent.setStatus("sending...");
    messagesData.appendTransient({ id: genId(), role: "user", text: [text, image ? `[Image: ${image.name}]` : "", attachmentSummary].filter(Boolean).join("\n") });
    try {
      const payload = image ? {
        content: [...(contextualText ? [{ type: "text", text: contextualText }] : []), { type: "image", artifactId: image.id }],
      } : { text: contextualText };
      const selectedRole = roles.find((role) => role.id === selectedRoleId) ?? null;
      const endpoint = `/v1/sessions/${encodeURIComponent(sessionAtSend)}/invocations`;
      const body = selectedRole && selectedRole.currentVersionId
        ? { ...payload, target: { kind: "role", objectId: selectedRole.id,
            versionId: selectedRole.currentVersionId, contextMode: "room" } }
        : { ...withRunConfig(payload, toolPolicyProfileId, selectedModelId, thinkingEffort), target: { kind: "host" } };
      const turn = await apiFetch<TurnSubmission>(endpoint, {
        method: "POST", headers: { "Idempotency-Key": genId() }, body: JSON.stringify(body),
      });
      if (selectedSessionRef.current !== sessionAtSend) return;
      agent.setError(null);
      void agent.watchRun(turn.run as AgentRun);
    } catch (reason) {
      if (selectedSessionRef.current === sessionAtSend) {
        setPendingImage(image);
        setTextAttachments(attachments);
        agent.setError(errorMessage(reason, "Failed to send the message"));
      }
    }
  }, [selectedSession, pendingImage, textAttachments, messagesData, agent, selectedModelId,
    selectedRoleId, roles, setInputVersioned, thinkingEffort]);

  type ExpandResponse = {
    case: "matched"; name: string; text: string; diagnostics: { level: string; code: string; message: string }[];
  } | {
    case: "not_found"; name: string; diagnostics: { level: string; code: string; message: string }[];
  } | {
    case: "invalid_invocation"; diagnostics: never[];
  };

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

  // Graph invocation is global; the Worker derives Project and workspace from
  // the selected Session when it creates the Run.
  const invokeAgentFlow = useCallback(async (name: string, version?: number, rawParams?: string) => {
    if (!selectedSession) {
      agent.setError("Select a session before invoking a graph.");
      return false;
    }
    const inputs: Record<string, string> = {};
    if (rawParams) {
      for (const pair of rawParams.split(/\s+/)) {
        const [key, ...rest] = pair.split("=");
        if (key && rest.length > 0) inputs[key] = rest.join("=");
      }
    }
    try {
      await apiFetch<RunAgentFlow>(`/v1/graphs/${encodeURIComponent(name)}/runs`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          sessionId: selectedSession, name, version: version ?? 0, inputs,
        }),
      });
      setInputVersioned("");
      agent.setError(null);
      return true;
    } catch (reason) {
      agent.setError(reason instanceof Error ? reason.message : "Failed to invoke graph");
      return false;
    }
  }, [selectedSession, agent, setInputVersioned]);

  const policyId = selectedPermissionPolicyID();

  const submit = useCallback(async () => {
    if (!selectedSession || (!input.trim() && !pendingImage && textAttachments.length === 0) || agent.activeRunID) return;

    // Graph addressing gates run BEFORE the normal turn: an explicit
    // /invoke_agent_flow command or a leading @graph:name[@version] token is a
    // Host orchestration call, never a chat message.
    const invokeMatch = input.trim().match(/^\/invoke_agent_flow\s+(\S+)(?:\s+([\s\S]+))?$/);
    if (invokeMatch) {
      const [name, version] = invokeMatch[1].split("@");
      void invokeAgentFlow(name, version ? Number(version) : undefined, invokeMatch[2]);
      return;
    }
    const flowToken = input.trim().match(/^@graph:([\w.-]+)(?:@(\d+))?(?:\s+([\s\S]+))?$/);
    if (flowToken) {
      void invokeAgentFlow(flowToken[1], flowToken[2] ? Number(flowToken[2]) : undefined, flowToken[3]);
      return;
    }

    if (!policyId && !selectedRoleId) {
      agent.setError(`The ${permissionMode} permission policy is unavailable.`);
      return;
    }

    // Slash expansion gate: intercept drafts starting with "/".
    if (input.startsWith("/") && expandedVersion !== draftVersion && selectedProject) {
      setExpanding(true);
      setExpandDiag(null);
      const actionProject = selectedProject;
      const actionDraftVer = draftVersion;
      const actionSession = selectedSession;
      try {
        const result = await handleExpand(input, selectedProject);
        if (!result) return; // aborted
        // Stale-guard: context changed during request.
        if (selectedProject !== actionProject || selectedSession !== actionSession || draftVersion !== actionDraftVer) {
          return;
        }
        switch (result.case) {
          case "matched": {
            const text = result.text.trim();
            if (!text) {
              agent.setError("Expanded prompt is empty.");
              break;
            }
            setInput(text);
            setDraftVersion((v) => v + 1);
            setExpandedVersion(draftVersion + 1);
            if (result.diagnostics.some((d) => d.code === "arguments_fallback")) {
              setExpandDiag("Arguments could not be fully parsed; using raw input.");
            }
            break;
          }
          case "not_found":
          case "invalid_invocation":
            // Fall through: send the original draft as a normal message.
            void sendTurn(input, policyId ?? "");
            break;
        }
      } catch (err: unknown) {
        agent.setError(errorMessage(err, "Failed to expand prompt template"));
      } finally {
        setExpanding(false);
      }
      return;
    }

    void sendTurn(input, policyId ?? "");
  }, [selectedSession, input, pendingImage, textAttachments.length, policyId, permissionMode,
     agent, sendTurn, selectedProject, selectedRoleId, expandedVersion, draftVersion, handleExpand, invokeAgentFlow]);

  const steer = useCallback(async () => {
    if (!agent.activeRunID || !input.trim()) return;

    // Same slash expansion gate for steer.
    if (input.startsWith("/") && expandedVersion !== draftVersion && selectedProject) {
      setExpanding(true);
      setExpandDiag(null);
      const actionProject = selectedProject;
      const actionDraftVer = draftVersion;
      const actionSession = selectedSession;
      try {
        const result = await handleExpand(input, selectedProject);
        if (!result) return;
        if (selectedProject !== actionProject || selectedSession !== actionSession || draftVersion !== actionDraftVer) return;
        switch (result.case) {
          case "matched": {
            const text = result.text.trim();
            if (!text) { agent.setError("Expanded prompt is empty."); break; }
            setInput(text);
            setDraftVersion((v) => v + 1);
            setExpandedVersion(draftVersion + 1);
            break;
          }
          case "not_found":
          case "invalid_invocation": {
            const text = input;
            setInputVersioned("");
            const queued = await agent.steer(text);
            if (!queued) setInputVersioned(text);
            break;
          }
        }
      } catch (err: unknown) {
        agent.setError(errorMessage(err, "Failed to expand"));
      } finally {
        setExpanding(false);
      }
      return;
    }

    const text = input;
    setInputVersioned("");
    const queued = await agent.steer(text);
    if (!queued) setInputVersioned(text);
  }, [agent, input, selectedProject, expandedVersion, draftVersion, handleExpand, selectedSession, setInputVersioned]);

  const followUp = useCallback(async () => {
    if (!agent.activeRunID || !input.trim()) return;
    const text = input;
    setInputVersioned("");
    const { queued, runEnded } = await agent.followUp(text);
    if (queued) return;
    // The run ended between click and submit: send as a normal new turn.
    if (runEnded && selectedSession) {
      void sendTurn(text, policyId ?? "");
      return;
    }
    setInputVersioned(text);
  }, [agent, input, sendTurn, policyId, selectedSession, setInputVersioned]);

  const uploadImage = useCallback(async (file: File) => {
    if (!selectedProject || !selectedSession) return;
    const data = new FormData();
    data.set("sessionId", selectedSession);
    data.set("file", file);
    try {
      agent.setStatus("uploading image...");
      const artifact = await apiFetch<ImageArtifact>(`/v1/projects/${encodeURIComponent(selectedProject)}/attachments/images`, {
        method: "POST", body: data,
      });
      setPendingImage(artifact);
      agent.setError(null);
    } catch (reason) {
      agent.setError(errorMessage(reason, "Failed to attach the image"));
    } finally {
      agent.setStatus("");
    }
  }, [selectedProject, selectedSession, agent]);

  const attachFiles = useCallback(async (files: File[]) => {
    if (!selectedSession) return;
    const images = files.filter((file) => file.type.startsWith("image/"));
    const documents = files.filter((file) => !file.type.startsWith("image/"));
    if (images[0]) await uploadImage(images[0]);
    if (images.length > 1) agent.setError("Only one image can be attached to a turn.");

    const accepted: TextAttachment[] = [];
    for (const file of documents) {
      if (!isSupportedTextAttachment(file)) {
        agent.setError(`${file.name} is not a supported text attachment.`);
        continue;
      }
      if (file.size > 1 << 20) {
        agent.setError(`${file.name} exceeds the 1 MiB text attachment limit.`);
        continue;
      }
      accepted.push({ id: genId(), name: file.name, size: file.size, text: await file.text() });
    }
    if (accepted.length) {
      setTextAttachments((current) => [...current, ...accepted].slice(0, 3));
      if (textAttachments.length + accepted.length > 3) agent.setError("A turn can include at most three text files.");
    }
  }, [selectedSession, agent, textAttachments.length, uploadImage]);

  const removeTextAttachment = useCallback((id: string) => {
    setTextAttachments((current) => current.filter((item) => item.id !== id));
  }, []);

  const selectModel = useCallback((modelId: string) => {
    if (!selectedSession) return;
    setModelOverrides((current) => ({ ...current, [selectedSession]: modelId }));
  }, [selectedSession]);

  const startCompaction = useCallback(async () => {
    if (!selectedSession || agent.activeRunID) return;
    const session = await refreshSelectedSession();
    if (!session?.activeLeafMessageId) {
      agent.setError("This session has no conversation history to compact.");
      return;
    }
    setCompactionPrompt({ open: true, instructions: "", busy: false });
  }, [agent, refreshSelectedSession, selectedSession]);

  const confirmCompaction = useCallback(async () => {
    if (!selectedSession || agent.activeRunID) return;
    const instructions = compactionPrompt.instructions.trim();
    setCompactionPrompt((current) => ({ ...current, busy: true }));
    try {
      const session = await refreshSelectedSession();
      if (!session?.activeLeafMessageId) {
        agent.setError("This session has no conversation history to compact.");
        setCompactionPrompt({ open: false, instructions: "", busy: false });
        return;
      }
      const submission = await apiFetch<CompactionSubmission>(`/v1/sessions/${encodeURIComponent(selectedSession)}/compactions`, {
        method: "POST", headers: { "Idempotency-Key": genId() },
        body: JSON.stringify({ baseMessageId: session.activeLeafMessageId, instructions }),
      });
      const run = await apiFetch<AgentRun>(`/v1/runs/${encodeURIComponent(submission.runId)}`);
      agent.setError(null);
      setCompactionPrompt({ open: false, instructions: "", busy: false });
      void agent.watchRun(run);
    } catch (reason) {
      agent.setError((reason as Error).message);
      setCompactionPrompt((current) => ({ ...current, busy: false }));
    }
  }, [agent, compactionPrompt.instructions, refreshSelectedSession, selectedSession]);

  const cancelCompaction = useCallback(() => {
    setCompactionPrompt((current) =>
      current.busy ? current : { open: false, instructions: "", busy: false });
  }, []);

  const createBranch = async (messageId: string) => {
    if (!agent.activeRunID) await branchesData.createBranch(messageId);
  };
  const activateBranch = async (branchId: string) => {
    if (!agent.activeRunID) await branchesData.activateBranch(branchId);
  };
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

  // ——— Views ———

  const history: HistoryView = {
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

  const run: RunView = {
    activeRun: agent.activeRunID,
    activeRunStatus: agent.activeRun?.status,
    status: agent.status,
    compacting: agent.compacting,
    pendingApproval: agent.pendingApproval,
    resolvingApproval: agent.resolvingApproval,
    decideApproval: (decision) => void agent.decideApproval(decision),
    cancel: () => void agent.cancel(),
    pendingFollowUps: agent.pendingFollowUps,
    recovery: recoveryData.recovery,
    retrying: recoveryData.retrying,
    retryRun: () => void retryRun(),
    error: agent.error,
    setError: agent.setError,
    clearError: () => agent.setError(null),
  };

  const branches: BranchView = {
    branches: branchesData.branches,
    activeBranchId,
    loading: branchesData.loading,
    changing: branchesData.changing,
    error: branchesData.error,
    createBranch: (messageId) => void createBranch(messageId),
    activateBranch: (branchId) => void activateBranch(branchId),
  };

  const composer: ComposerView = {
    sessionId: selectedSession,
    activeLeafMessageId,
    input,
    setInput: setInputVersioned,
    pendingImage,
    clearPendingImage: () => setPendingImage(null),
    uploadImage: (file) => void uploadImage(file),
    textAttachments,
    removeTextAttachment,
    attachFiles: (files) => void attachFiles(files),
    models: settings.models.filter((model) => model.status === "active"),
    selectedModelId,
    setSelectedModelId: selectModel,
    thinkingEffort,
    setThinkingEffort,
    roles,
    selectedRoleId,
    setSelectedRoleId,
    permissionMode,
    displayedPermissionMode: displayedPermissionMode(),
    permissionReady: Boolean(policyId),
    setPermissionMode,
    compactSession: () => void startCompaction(),
    compaction: {
      open: compactionPrompt.open,
      instructions: compactionPrompt.instructions,
      busy: compactionPrompt.busy,
      setInstructions: (value) => setCompactionPrompt((current) => ({ ...current, instructions: value })),
      confirm: () => void confirmCompaction(),
      cancel: cancelCompaction,
    },
    promptPanel: {
      templates: promptCatalog.templates,
      roles: roles.map((role) => ({ id: role.id, handle: role.handle, name: role.name, description: role.description })),
      flows: flowCatalog,
      show: commandPanelOpen && !promptPanelDismissed,
      onSelect: (name: string) => {
        setPromptPanelDismissed(false);
        setInputVersioned(`/${name} `);
      },
      onRoleSelect: (roleId: string) => {
        setSelectedRoleId(roleId);
        setPromptPanelDismissed(true);
        setInputVersioned("");
      },
      onFlowSelect: (name: string, version?: number) => {
        setPromptPanelDismissed(false);
        setInputVersioned(`@graph:${name}${version ? `@${version}` : ""} `);
      },
      onClose: () => setPromptPanelDismissed(true),
      expanding,
      expandDiag,
    },
  };

  return {
    history,
    run,
    branches,
    composer,
    actions: { submit, steer, followUp },
    error,
    clearError,
  };
}
