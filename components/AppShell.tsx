"use client";

import { useState, useCallback, useRef, useEffect, useSyncExternalStore } from "react";
import { SessionSidebar } from "./SessionSidebar";
import { ChatWindow } from "./ChatWindow";
import type { TextAttachment } from "./Composer";
import type { RunAgentFlow } from "@/components/settings/types";
import { TopBar } from "./TopBar";
import { RightPanel } from "./RightPanel";
import { FilePreviewWindow } from "./FilePreviewWindow";
import { ResizeHandle } from "./ResizeHandle";
import type { Tab } from "./TabBar";
import { ProjectCreateDialog } from "./ProjectCreateDialog";
import { SettingsDialog } from "./settings/SettingsDialog";
import { useResizable } from "@/hooks/useResizable";
import { useFileTabs } from "@/hooks/useFileTabs";
import { useSessionTitle } from "@/hooks/useSessionTitle";
import { useProjectSessions } from "@/hooks/useProjectSessions";
import { useSidebarProjectGroups } from "@/hooks/useSidebarProjectGroups";
import { useSessionBranches } from "@/hooks/useSessionBranches";
import { useSessionMessages } from "@/hooks/useSessionMessages";
import { useAgentSession } from "@/hooks/useAgentSession";
import { ChildProgressProvider } from "@/hooks/useChildProgress";
import { useWorkspace } from "./WorkspaceProvider";
import { useRunRecovery } from "@/hooks/useRunRecovery";
import { useRunningSessionIds } from "@/hooks/useRunningSessionIds";
import { useSettingsProfiles } from "@/hooks/useSettingsProfiles";
import { usePromptTemplates } from "@/hooks/usePromptTemplates";
import { permissionModeForPolicyID, permissionPolicyID, withRunConfig, type PermissionMode, type ThinkingEffort } from "@/lib/permission-mode";
import { errorMessage } from "@/lib/provider-errors";
import { apiFetch } from "@/lib/worker-api.client";
import type { RoleSummary, Session } from "@/components/settings/types";
import type { AgentRun } from "@/lib/approval";
import type { components } from "@/lib/worker-api.gen";

type ProjectWorkspace = components["schemas"]["ProjectWorkspace"];
type TurnSubmission = components["schemas"]["TurnSubmission"];
type ImageArtifact = components["schemas"]["ImageArtifact"];
type CompactionSubmission = components["schemas"]["CompactionSubmission"];
type GlobalRoleSummary = components["schemas"]["GlobalRoleSummary"];
type GlobalRoleDetail = components["schemas"]["GlobalRoleDetail"];
type FileRevision = { version: number; publishedAt: string };

function genId(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}

function subscribeMobile(listener: () => void) {
  const media = window.matchMedia("(max-width: 640px)");
  media.addEventListener("change", listener);
  return () => media.removeEventListener("change", listener);
}

function useIsMobile() {
  return useSyncExternalStore(subscribeMobile, () => window.matchMedia("(max-width: 640px)").matches, () => false);
}

export function AppShell() {
  const isMobile = useIsMobile();
  const {
    projects, selectedProject, switchProject: workspaceSwitchProject,
    createProjectOpen, openCreateProject, confirmCreateProject, cancelCreateProject, createProjectBusy,
    settingsOpen, openSettings: workspaceOpenSettings, closeSettings,
    workspaceFor, togglePinProject, pinnedProjectIds,
  } = useWorkspace();
  // Session/chat state stays in the chat shell.
  const [selectedSession, setSelectedSessionState] = useState<string | null>(null);
  const [roles, setRoles] = useState<RoleSummary[]>([]);
  const [selectedRoleId, setSelectedRoleId] = useState<string | null>(null);
  const [input, setInput] = useState("");
  const [compactionPrompt, setCompactionPrompt] = useState<{ open: boolean; instructions: string; busy: boolean }>({
    open: false, instructions: "", busy: false,
  });
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [pendingImage, setPendingImage] = useState<ImageArtifact | null>(null);
  const [textAttachments, setTextAttachments] = useState<TextAttachment[]>([]);
  const [modelOverrides, setModelOverrides] = useState<Record<string, string>>({});
  const [permissionMode, setPermissionMode] = useState<PermissionMode>("discuss");
  const [thinkingEffort, setThinkingEffort] = useState<ThinkingEffort>("default");
  const selectedSessionRef = useRef<string | null>(selectedSession);

  // Prompt template expansion state.
  const [draftVersion, setDraftVersion] = useState(0);
  const [expandedVersion, setExpandedVersion] = useState<number | null>(null);
  const [expanding, setExpanding] = useState(false);
  const [expandDiag, setExpandDiag] = useState<string | null>(null);
  const [promptPanelDismissed, setPromptPanelDismissed] = useState(false);
  const expandAbortRef = useRef<AbortController | null>(null);
  const promptCatalog = usePromptTemplates(selectedProject);
  const [flowCatalog, setFlowCatalog] = useState<{ name: string; version?: number }[]>([]);
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

  // Refresh the catalog whenever the command panel transitions from closed to
  // open, so external file changes are visible (design §9.1).
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

  const selectSession = useCallback((sessionId: string | null) => {
    selectedSessionRef.current = sessionId;
    setSelectedSessionState(sessionId);
    setInputVersioned("");
    setPendingImage(null);
    setTextAttachments([]);
  }, [setInputVersioned]);

  // Resizable panels
  const sidebarResize = useResizable({ initialWidth: 260, minWidth: 180, maxWidth: 500, storageKey: "--ennote-sidebar-width" });
  const rightPanelResize = useResizable({ initialWidth: 420, minWidth: 280, maxWidth: 1000, storageKey: "--ennote-right-panel-width", direction: "left" });

  // Right panel layout state (file tabs live in useFileTabs)
  const [rightPanelOpen, setRightPanelOpen] = useState(false);
  const [previewFile, setPreviewFile] = useState<{ projectId: string; path: string; name: string } | null>(null);

  // Top bar
  const navigationTriggerRef = useRef<HTMLButtonElement>(null);

  const closeMobileNavigation = useCallback(() => {
    setSidebarOpen(false);
    window.requestAnimationFrame(() => navigationTriggerRef.current?.focus());
  }, []);

  useEffect(() => {
    if (!isMobile) return;
    const frame = window.requestAnimationFrame(() => setSidebarOpen(false));
    return () => window.cancelAnimationFrame(frame);
  }, [isMobile]);

  useEffect(() => {
    if (!isMobile || !sidebarOpen) return;
    const navigation = document.querySelector<HTMLElement>(".sidebar-container");
    if (!navigation) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeMobileNavigation();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = Array.from(navigation.querySelectorAll<HTMLElement>(
        'button:not(:disabled), input:not(:disabled), select:not(:disabled), [href], [tabindex]:not([tabindex="-1"])',
      ));
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [closeMobileNavigation, isMobile, sidebarOpen]);

  // Session data
  const sessionNavigation = useProjectSessions(selectedProject);
  const sidebarGroups = useSidebarProjectGroups(projects, pinnedProjectIds, selectedProject);
  const settings = useSettingsProfiles();
  const replaceSession = sessionNavigation.replaceSession;
  const selectedSessionRecord = sessionNavigation.activeSessions.find(s => s.id === selectedSession);
  const selectedModelId = selectedSession
    ? modelOverrides[selectedSession]
      ?? selectedSessionRecord?.defaultModelProfileId
      ?? settings.models.find((model) => model.isDefault)?.id
      ?? settings.models[0]?.id
      ?? null
    : null;
  const activeBranchId = selectedSessionRecord?.activeBranchId;
  const updateSession = useCallback((session: Session) => replaceSession(session), [replaceSession]);

  const branches = useSessionBranches({ sessionId: selectedSession, activeBranchId, onSessionUpdated: updateSession });
  const {
    messages, loading: historyLoading, loadingOlder: historyLoadingOlder, historyError,
    hasMore: hasMoreHistory, loadOlder: loadOlderHistory, refreshLatest,
    appendTransient: addMsg, upsertTransient: upsertMsg,
  } = useSessionMessages(selectedSession, activeBranchId);

  const refreshSelectedSession = useCallback(async () => {
    if (!selectedSession) return null;
    const current = await apiFetch<Session>(`/v1/sessions/${encodeURIComponent(selectedSession)}`);
    replaceSession(current);
    return current;
  }, [replaceSession, selectedSession]);

  const {
    activeRun: activeRunRecord, activeRunID: activeRun, compacting, pendingApproval, resolvingApproval,
    status, setStatus, error, setError, watchRun, cancel, steer: queueSteer, followUp: queueFollowUp,
    pendingFollowUps, decideApproval,
  } = useAgentSession({
    sessionId: selectedSession, lineageId: activeBranchId, appendMessage: addMsg, upsertMessage: upsertMsg,
    refreshLatest, refreshSession: refreshSelectedSession,
  });
  const recovery = useRunRecovery(selectedSession, activeBranchId, activeRun);
  const runningSessionIds = useRunningSessionIds(
    sidebarGroups.groups.flatMap((group) => group.sessions.map((session) => session.id)),
    activeRun ? selectedSession : null,
  );

  const selectedPermissionPolicyID = useCallback(
    () => { return permissionPolicyID(settings.policies, permissionMode); },
    [permissionMode, settings.policies],
  );
  const displayedPermissionMode = useCallback(() => {
    const requested = activeRunRecord?.requestedConfig as Record<string, unknown> | undefined;
    return permissionModeForPolicyID(settings.policies, requested?.toolPolicyProfileId) ?? permissionMode;
  }, [activeRunRecord, permissionMode, settings.policies]);

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

  const refreshSettings = settings.refresh;
  const openSettings = useCallback(() => {
    workspaceOpenSettings();
    void refreshSettings();
  }, [refreshSettings, workspaceOpenSettings]);

  const switchProject = useCallback((projectId: string) => {
    workspaceSwitchProject(projectId);
    setSelectedRoleId(null);
    selectSession(null);
    sessionNavigation.setView("active");
    sessionNavigation.setQuery("");
    if (isMobile) closeMobileNavigation();
  }, [closeMobileNavigation, isMobile, selectSession, sessionNavigation, workspaceSwitchProject]);

  const createSession = useCallback(async () => {
    if (!selectedProject) return;
    try {
      const session = await apiFetch<Session>(`/v1/projects/${encodeURIComponent(selectedProject)}/sessions`, {
        method: "POST", body: JSON.stringify({ title: `Chat ${new Date().toLocaleTimeString()}` }),
      });
      setError(null);
      sessionNavigation.replaceSession(session);
      selectSession(session.id);
      await sessionNavigation.refresh();
      sidebarGroups.refresh(selectedProject);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [selectSession, selectedProject, sessionNavigation, setError, sidebarGroups]);

  const switchSession = useCallback((sessionId: string) => {
    closeSettings();
    selectSession(sessionId);
    apiFetch<Session>(`/v1/sessions/${encodeURIComponent(sessionId)}`).then(sessionNavigation.replaceSession).catch(() => {});
    if (isMobile) closeMobileNavigation();
  }, [closeMobileNavigation, closeSettings, isMobile, selectSession, sessionNavigation]);

  const archiveSession = useCallback(async (session: Session) => {
    const succeeded = await sessionNavigation.archive(session);
    if (succeeded) {
      sidebarGroups.refresh(session.projectId);
      sidebarGroups.refreshArchived(session.projectId);
    }
    if (succeeded && selectedSessionRef.current === session.id) selectSession(null);
  }, [selectSession, sessionNavigation, sidebarGroups]);

  const restoreSession = useCallback(async (session: Session) => {
    await sessionNavigation.restore(session);
    sidebarGroups.refresh(session.projectId);
    sidebarGroups.refreshArchived(session.projectId);
  }, [sessionNavigation, sidebarGroups]);

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
    setStatus("sending...");
    addMsg({ id: genId(), role: "user", text: [text, image ? `[Image: ${image.name}]` : "", attachmentSummary].filter(Boolean).join("\n") });
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
      setError(null);
      void watchRun(turn.run as AgentRun);
    } catch (reason) {
      if (selectedSessionRef.current === sessionAtSend) {
        setPendingImage(image);
        setTextAttachments(attachments);
        setError(errorMessage(reason, "Failed to send the message"));
      }
    }
  }, [selectedSession, pendingImage, textAttachments, addMsg, setError, setStatus, watchRun, selectedModelId,
    selectedRoleId, roles, setInputVersioned, thinkingEffort]);

  // ——— Prompt template expansion ———

  const policyId = selectedPermissionPolicyID();

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
      setError("Select a session before invoking a graph.");
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
      setError(null);
      return true;
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Failed to invoke graph");
      return false;
    }
  }, [selectedSession, setError, setInputVersioned]);

  const submit = useCallback(async () => {
    if (!selectedSession || (!input.trim() && !pendingImage && textAttachments.length === 0) || activeRun) return;

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
      setError(`The ${permissionMode} permission policy is unavailable.`);
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
              setError("Expanded prompt is empty.");
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
        setError(errorMessage(err, "Failed to expand prompt template"));
      } finally {
        setExpanding(false);
      }
      return;
    }

    void sendTurn(input, policyId ?? "");
  }, [selectedSession, input, pendingImage, textAttachments.length, activeRun, policyId, permissionMode, sendTurn, setError,
     selectedProject, selectedRoleId, expandedVersion, draftVersion, handleExpand, invokeAgentFlow]);

  const steer = useCallback(async () => {
    if (!activeRun || !input.trim()) return;

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
            if (!text) { setError("Expanded prompt is empty."); break; }
            setInput(text);
            setDraftVersion((v) => v + 1);
            setExpandedVersion(draftVersion + 1);
            break;
          }
          case "not_found":
          case "invalid_invocation": {
            const text = input;
            setInputVersioned("");
            const queued = await queueSteer(text);
            if (!queued) setInputVersioned(text);
            break;
          }
        }
      } catch (err: unknown) {
        setError(errorMessage(err, "Failed to expand"));
      } finally {
        setExpanding(false);
      }
      return;
    }

    const text = input;
    setInputVersioned("");
    const queued = await queueSteer(text);
    if (!queued) setInputVersioned(text);
  }, [activeRun, input, queueSteer, selectedProject, expandedVersion, draftVersion, handleExpand, selectedSession, setError, setInputVersioned]);

  const followUp = useCallback(async () => {
    if (!activeRun || !input.trim()) return;
    const text = input;
    setInputVersioned("");
    const { queued, runEnded } = await queueFollowUp(text);
    if (queued) return;
    // The run ended between click and submit: send as a normal new turn.
    if (runEnded && selectedSession) {
      void sendTurn(text, policyId ?? "");
      return;
    }
    setInputVersioned(text);
  }, [activeRun, input, queueFollowUp, sendTurn, policyId, selectedSession, setInputVersioned]);

  const uploadImage = useCallback(async (file: File) => {
    if (!selectedProject || !selectedSession) return;
    const data = new FormData();
    data.set("sessionId", selectedSession);
    data.set("file", file);
    try {
      setStatus("uploading image...");
      const artifact = await apiFetch<ImageArtifact>(`/v1/projects/${encodeURIComponent(selectedProject)}/attachments/images`, {
        method: "POST", body: data,
      });
      setPendingImage(artifact);
      setError(null);
    } catch (reason) {
      setError(errorMessage(reason, "Failed to attach the image"));
    } finally {
      setStatus("");
    }
  }, [selectedProject, selectedSession, setError, setStatus]);

  const attachFiles = useCallback(async (files: File[]) => {
    if (!selectedSession) return;
    const images = files.filter((file) => file.type.startsWith("image/"));
    const documents = files.filter((file) => !file.type.startsWith("image/"));
    if (images[0]) await uploadImage(images[0]);
    if (images.length > 1) setError("Only one image can be attached to a turn.");

    const accepted: TextAttachment[] = [];
    for (const file of documents) {
      if (!isSupportedTextAttachment(file)) {
        setError(`${file.name} is not a supported text attachment.`);
        continue;
      }
      if (file.size > 1 << 20) {
        setError(`${file.name} exceeds the 1 MiB text attachment limit.`);
        continue;
      }
      accepted.push({ id: genId(), name: file.name, size: file.size, text: await file.text() });
    }
    if (accepted.length) {
      setTextAttachments((current) => [...current, ...accepted].slice(0, 3));
      if (textAttachments.length + accepted.length > 3) setError("A turn can include at most three text files.");
    }
  }, [selectedSession, setError, textAttachments.length, uploadImage]);

  const removeTextAttachment = useCallback((id: string) => {
    setTextAttachments((current) => current.filter((item) => item.id !== id));
  }, []);

  const selectModel = useCallback((modelId: string) => {
    if (!selectedSession) return;
    setModelOverrides((current) => ({ ...current, [selectedSession]: modelId }));
  }, [selectedSession]);

  const startCompaction = useCallback(async () => {
    if (!selectedSession || activeRun) return;
    const session = await refreshSelectedSession();
    if (!session?.activeLeafMessageId) {
      setError("This session has no conversation history to compact.");
      return;
    }
    setCompactionPrompt({ open: true, instructions: "", busy: false });
  }, [activeRun, refreshSelectedSession, selectedSession, setError]);

  const confirmCompaction = useCallback(async () => {
    if (!selectedSession || activeRun) return;
    const instructions = compactionPrompt.instructions.trim();
    setCompactionPrompt((current) => ({ ...current, busy: true }));
    try {
      const session = await refreshSelectedSession();
      if (!session?.activeLeafMessageId) {
        setError("This session has no conversation history to compact.");
        setCompactionPrompt({ open: false, instructions: "", busy: false });
        return;
      }
      const submission = await apiFetch<CompactionSubmission>(`/v1/sessions/${encodeURIComponent(selectedSession)}/compactions`, {
        method: "POST", headers: { "Idempotency-Key": genId() },
        body: JSON.stringify({ baseMessageId: session.activeLeafMessageId, instructions }),
      });
      const run = await apiFetch<AgentRun>(`/v1/runs/${encodeURIComponent(submission.runId)}`);
      setError(null);
      setCompactionPrompt({ open: false, instructions: "", busy: false });
      void watchRun(run);
    } catch (reason) {
      setError((reason as Error).message);
      setCompactionPrompt((current) => ({ ...current, busy: false }));
    }
  }, [activeRun, compactionPrompt.instructions, refreshSelectedSession, selectedSession, setError, watchRun]);

  const cancelCompaction = useCallback(() => {
    setCompactionPrompt((current) =>
      current.busy ? current : { open: false, instructions: "", busy: false });
  }, []);

  const createBranch = useCallback(async (messageId: string) => {
    if (!activeRun) await branches.createBranch(messageId);
  }, [activeRun, branches]);
  const activateBranch = useCallback(async (branchId: string) => {
    if (!activeRun) await branches.activateBranch(branchId);
  }, [activeRun, branches]);
  const retryRun = useCallback(async () => {
    const run = await recovery.retry();
    if (run) void watchRun(run);
  }, [recovery, watchRun]);

  // File operations use project-scoped /workspace paths. Host paths are display-only.
  const currentProjectId = selectedSessionRecord?.projectId ?? selectedProject;
  const currentWorkspace = currentProjectId ? workspaceFor(currentProjectId) ?? null : null;
  const currentCwd = currentWorkspace?.hostPath ?? null;

  // Workspace loading is owned by the WorkspaceProvider; surface load errors here.
  useEffect(() => {
    const projectId = currentProjectId;
    if (!projectId || workspaceFor(projectId)) return;
    const controller = new AbortController();
    void apiFetch<ProjectWorkspace>(`/v1/projects/${encodeURIComponent(projectId)}/workspace`, { signal: controller.signal })
      .catch((reason) => {
        if (!controller.signal.aborted) setError((reason as Error).message);
      });
    return () => controller.abort();
    // workspaceFor is stable via context; re-run on project switch.
  }, [currentProjectId, setError, workspaceFor]);

  const fileTabsState = useFileTabs(currentProjectId);
  const { fileTabs, activeTabId: activeRightTabId, setActiveTabId: setActiveRightTabId, openFile, closeTab } = fileTabsState;

  const handleOpenFile = useCallback((filePath: string, fileName: string) => {
    openFile(filePath, fileName);
    setRightPanelOpen(true);
  }, [openFile, setRightPanelOpen]);

  const handlePreviewFile = useCallback((filePath: string, fileName: string) => {
    if (!currentProjectId) return;
    setPreviewFile({ projectId: currentProjectId, path: filePath, name: fileName });
  }, [currentProjectId]);

  // Title editing
  const titleEdit = useSessionTitle({ session: selectedSessionRecord, replaceSession: sessionNavigation.replaceSession });

  const topBarProjectName = currentCwd
    ? currentCwd.replace(/\/+$/, "").split(/[\\/]/).filter(Boolean).pop() || currentCwd
    : "";

  // Right tabs
  const rightTabs: Tab[] = [
    { id: "files", label: "Files", closable: false, icon: "files" },
    { id: "graph", label: "Graphs", closable: false, icon: "graph" },
    { id: "tools", label: "Status", closable: false, icon: "tools" },
    ...fileTabs,
  ];

  // Sidebar content
  const sidebar = (
    <SessionSidebar
      projects={projects}
      groups={sidebarGroups.groups}
      selectedProject={selectedProject}
      selectedSession={selectedSession}
      settingsOpen={settingsOpen}
      query={sessionNavigation.query}
      setQuery={sessionNavigation.setQuery}
      mutatingId={sessionNavigation.mutatingId}
      announcement={sessionNavigation.announcement}
      pinnedProjectIds={pinnedProjectIds}
      togglePinProject={togglePinProject}
      collapsed={sidebarGroups.collapsed}
      toggleCollapsed={sidebarGroups.toggleCollapsed}
      archived={sidebarGroups.archived}
      openArchived={sidebarGroups.openArchived}
      refreshGroups={sidebarGroups.refreshAll}
      createProject={openCreateProject}
      createSession={createSession}
      switchProject={switchProject}
      switchSession={switchSession}
      archiveSession={session => void archiveSession(session)}
      restoreSession={session => void restoreSession(session)}
      openSettings={openSettings}
      closeNavigation={closeMobileNavigation}
      runningSessionIds={runningSessionIds}
    />
  );

  const combinedError = error ?? branches.error ?? recovery.error ?? sessionNavigation.error;
  const clearCombinedError = () => { setError(null); branches.clearError(); recovery.clearError(); sessionNavigation.setError(null); };

  return (
    <ChildProgressProvider>
    <>
      <div className="app-shell" data-testid="ennote-shell">
        {/* Mobile overlay backdrop */}
        <div
          className="sidebar-overlay-backdrop"
          onClick={closeMobileNavigation}
          style={{ opacity: sidebarOpen ? 1 : 0, pointerEvents: sidebarOpen ? "auto" : "none" }}
        />

        {/* Left sidebar */}
        <div
          className={`sidebar-container${sidebarOpen ? " sidebar-open" : " sidebar-closed"}`}
          role={isMobile && sidebarOpen ? "dialog" : undefined}
          aria-modal={isMobile && sidebarOpen ? "true" : undefined}
          aria-label={isMobile && sidebarOpen ? "Navigation" : undefined}
          id="workspace-navigation"
          style={{
            width: sidebarResize.width,
            minWidth: sidebarResize.width,
            transition: sidebarOpen || sidebarResize.isResizing ? "none" : undefined,
          }}
        >
          {sidebar}
        </div>

        {sidebarOpen && (
          <ResizeHandle
            side="right"
            ariaLabel="Resize sidebar"
            value={sidebarResize.width}
            min={180}
            max={500}
            onResizeStart={sidebarResize.beginResize}
            onResize={sidebarResize.resizeBy}
            onResizeEnd={sidebarResize.endResize}
          />
        )}

        {/* Center: chat */}
        <div className="workspace-content" inert={isMobile && sidebarOpen} style={{ flex: 1, display: "flex", flexDirection: "column", overflow: "hidden", minWidth: 0 }}>
          {/* Top bar */}
          <TopBar
            sidebarOpen={sidebarOpen}
            onToggleSidebar={() => setSidebarOpen((v) => !v)}
            navigationTriggerRef={navigationTriggerRef}
            session={selectedSessionRecord}
            title={titleEdit}
            projectName={topBarProjectName}
            projectPath={currentCwd}
            branch={{
              branches: branches.branches,
              activeBranchId,
              loading: branches.loading,
              changing: branches.changing,
              disabled: Boolean(activeRun),
              onActivate: (branchId) => void activateBranch(branchId),
            }}
            rightPanelOpen={rightPanelOpen}
            onToggleRightPanel={() => setRightPanelOpen((v) => !v)}
            attention={{
              projectId: selectedProject ?? currentProjectId ?? undefined,
              onNavigate: (item) => {
                if (item.sessionId && item.sessionId !== selectedSession) switchSession(item.sessionId);
              },
            }}
          />

          {/* Chat area */}
          <ChatWindow
            selectedSession={selectedSession}
            activeLeafMessageId={selectedSessionRecord?.activeLeafMessageId}
            activeBranchId={activeBranchId}
            branchChanging={branches.changing}
            createBranch={messageId => void createBranch(messageId)}
            recovery={recovery.recovery}
            retrying={recovery.retrying}
            retryRun={() => void retryRun()}
            messages={messages}
            historyLoading={historyLoading}
            historyLoadingOlder={historyLoadingOlder}
            historyError={historyError}
            hasMoreHistory={hasMoreHistory}
            loadOlderHistory={loadOlderHistory}
            error={combinedError}
            clearError={clearCombinedError}
            status={status}
            input={input}
            setInput={setInputVersioned}
            activeRun={activeRun}
            activeRunStatus={activeRunRecord?.status}
            compacting={compacting}
            permissionMode={displayedPermissionMode()}
            permissionReady={Boolean(policyId)}
            setPermissionMode={setPermissionMode}
            pendingApproval={pendingApproval}
            resolvingApproval={resolvingApproval}
            decideApproval={decision => void decideApproval(decision)}
            pendingImage={pendingImage}
            clearPendingImage={() => setPendingImage(null)}
            uploadImage={uploadImage}
            models={settings.models.filter((model) => model.status === "active")}
            selectedModelId={selectedModelId}
            setSelectedModelId={selectModel}
            thinkingEffort={thinkingEffort}
            setThinkingEffort={setThinkingEffort}
            roles={roles}
            selectedRoleId={selectedRoleId}
            setSelectedRoleId={setSelectedRoleId}
            textAttachments={textAttachments}
            removeTextAttachment={removeTextAttachment}
            attachFiles={files => void attachFiles(files)}
            submit={submit}
            steer={steer}
            followUp={followUp}
            cancel={cancel}
            pendingFollowUps={pendingFollowUps}
            compactSession={startCompaction}
            compactionPromptOpen={compactionPrompt.open}
            compactionInstructions={compactionPrompt.instructions}
            compactionBusy={compactionPrompt.busy}
            setCompactionInstructions={(value) =>
              setCompactionPrompt((current) => ({ ...current, instructions: value }))}
            confirmCompaction={confirmCompaction}
            cancelCompaction={cancelCompaction}
            promptTemplates={promptCatalog.templates}
            panelRoles={roles.map((role) => ({ id: role.id, handle: role.handle, name: role.name, description: role.description }))}
            panelFlows={flowCatalog}
            showPromptPanel={commandPanelOpen && !promptPanelDismissed}
            onPromptSelect={(name: string) => {
              setPromptPanelDismissed(false);
              setInputVersioned(`/${name} `);
            }}
            onRoleSelect={(roleId: string) => {
              setSelectedRoleId(roleId);
              setPromptPanelDismissed(true);
              setInputVersioned("");
            }}
            onFlowSelect={(name: string, version?: number) => {
              setPromptPanelDismissed(false);
              setInputVersioned(`@graph:${name}${version ? `@${version}` : ""} `);
            }}
            onPromptPanelClose={() => setPromptPanelDismissed(true)}
            expanding={expanding}
            expandDiag={expandDiag}
          />
        </div>

        {/* Right panel resize handle */}
        {rightPanelOpen && (
          <ResizeHandle
            side="left"
            ariaLabel="Resize right panel"
            value={rightPanelResize.width}
            min={280}
            max={1000}
            onResizeStart={rightPanelResize.beginResize}
            onResize={rightPanelResize.resizeBy}
            onResizeEnd={rightPanelResize.endResize}
          />
        )}

        {/* Right panel */}
        <RightPanel
          open={rightPanelOpen}
          onClose={() => setRightPanelOpen(false)}
          resize={rightPanelResize}
          tabs={rightTabs}
          activeTabId={activeRightTabId}
          onSelectTab={setActiveRightTabId}
          onCloseTab={closeTab}
          projectId={currentProjectId ?? null}
          displayPath={currentCwd}
          onOpenFile={handleOpenFile}
          onPreviewFile={handlePreviewFile}
          selectedSession={selectedSession}
          sessionTitle={selectedSessionRecord?.title || selectedSession || ""}
          activeRun={activeRun}
          status={status}
          permissionMode={permissionMode}
        />

      </div>

      {previewFile && (
        <FilePreviewWindow
          projectId={previewFile.projectId}
          filePath={previewFile.path}
          fileName={previewFile.name}
          onClose={() => setPreviewFile(null)}
        />
      )}

      {/* Settings dialog */}
      <SettingsDialog
        open={settingsOpen}
        onClose={closeSettings}
        providers={settings.providers}
        models={settings.models}
        policies={settings.policies}
        session={selectedSessionRecord}
        refresh={settings.refresh}
        error={settings.error}
        setError={settings.setError}
        onSessionUpdated={sessionNavigation.replaceSession}
        projectId={selectedProject}
      />

      {/* New project dialog */}
      {createProjectOpen && (
        <ProjectCreateDialog
          busy={createProjectBusy}
          error={error}
          onCreate={(name, hostPath) => void confirmCreateProject(name, hostPath)}
          onClose={cancelCreateProject}
        />
      )}
    </>
    </ChildProgressProvider>
  );
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
