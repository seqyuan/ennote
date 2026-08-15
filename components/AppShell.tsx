"use client";

import { useState, useCallback, useRef, useEffect, useSyncExternalStore } from "react";
import { SessionSidebar } from "./SessionSidebar";
import { ChatWindow } from "./ChatWindow";
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
import { useChatController } from "@/hooks/useChatController";
import { apiFetch } from "@/lib/worker-api.client";
import type { Session } from "@/components/settings/types";
import type { components } from "@/lib/worker-api.gen";

type ProjectWorkspace = components["schemas"]["ProjectWorkspace"];

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

  // Session selection stays in the chat shell; the composer reset that used to
  // live in selectSession is now declarative inside useChatController.
  const [selectedSession, setSelectedSessionState] = useState<string | null>(null);

  const selectSession = useCallback((sessionId: string | null) => {
    setSelectedSessionState(sessionId);
  }, []);

  // Resizable panels
  const sidebarResize = useResizable({ initialWidth: 260, minWidth: 180, maxWidth: 500, storageKey: "--ennote-sidebar-width" });
  const rightPanelResize = useResizable({ initialWidth: 420, minWidth: 280, maxWidth: 1000, storageKey: "--ennote-right-panel-width", direction: "left" });

  // Right panel layout state (file tabs live in useFileTabs)
  const [sidebarOpen, setSidebarOpen] = useState(true);
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

  // Session data (navigation/workspace domain stays here; the chat session
  // hooks are still called here and injected into useChatController as a
  // transitional step — the controller takes them over in a later step).
  const sessionNavigation = useProjectSessions(selectedProject);
  const sidebarGroups = useSidebarProjectGroups(projects, pinnedProjectIds, selectedProject);
  const settings = useSettingsProfiles();
  const replaceSession = sessionNavigation.replaceSession;
  const selectedSessionRecord = sessionNavigation.activeSessions.find(s => s.id === selectedSession);
  const activeBranchId = selectedSessionRecord?.activeBranchId;
  const updateSession = useCallback((session: Session) => replaceSession(session), [replaceSession]);

  const sessionBranches = useSessionBranches({ sessionId: selectedSession, activeBranchId, onSessionUpdated: updateSession });
  const sessionMessages = useSessionMessages(selectedSession, activeBranchId);

  const refreshSelectedSession = useCallback(async () => {
    if (!selectedSession) return null;
    const current = await apiFetch<Session>(`/v1/sessions/${encodeURIComponent(selectedSession)}`);
    replaceSession(current);
    return current;
  }, [replaceSession, selectedSession]);

  const agentSession = useAgentSession({
    sessionId: selectedSession, lineageId: activeBranchId, appendMessage: sessionMessages.appendTransient,
    upsertMessage: sessionMessages.upsertTransient, refreshLatest: sessionMessages.refreshLatest,
    refreshSession: refreshSelectedSession,
  });
  const recovery = useRunRecovery(selectedSession, activeBranchId, agentSession.activeRunID);
  const runningSessionIds = useRunningSessionIds(
    sidebarGroups.groups.flatMap((group) => group.sessions.map((session) => session.id)),
    agentSession.activeRunID ? selectedSession : null,
  );

  const promptCatalog = usePromptTemplates(selectedProject);

  // Chat domain: composer state + all chat actions live in the controller.
  const chat = useChatController({
    selectedSession,
    sessionRecord: selectedSessionRecord,
    selectedProject,
    promptCatalog,
    settings,
    replaceSession,
    sessionData: {
      messages: sessionMessages,
      agent: agentSession,
      recovery,
      branches: sessionBranches,
      refreshSelectedSession,
    },
  });

  const refreshSettings = settings.refresh;
  const openSettings = useCallback(() => {
    workspaceOpenSettings();
    void refreshSettings();
  }, [refreshSettings, workspaceOpenSettings]);

  const switchProject = useCallback((projectId: string) => {
    workspaceSwitchProject(projectId);
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
      chat.run.setError(null);
      sessionNavigation.replaceSession(session);
      selectSession(session.id);
      await sessionNavigation.refresh();
      sidebarGroups.refresh(selectedProject);
    } catch (reason) {
      chat.run.setError((reason as Error).message);
    }
  }, [selectSession, selectedProject, sessionNavigation, sidebarGroups, chat.run]);

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
    if (succeeded && selectedSession === session.id) selectSession(null);
  }, [selectSession, sessionNavigation, sidebarGroups, selectedSession]);

  const restoreSession = useCallback(async (session: Session) => {
    await sessionNavigation.restore(session);
    sidebarGroups.refresh(session.projectId);
    sidebarGroups.refreshArchived(session.projectId);
  }, [sessionNavigation, sidebarGroups]);

  // File operations use project-scoped /workspace paths. Host paths are display-only.
  const currentProjectId = selectedSessionRecord?.projectId ?? selectedProject;
  const currentWorkspace = currentProjectId ? workspaceFor(currentProjectId) ?? null : null;
  const currentCwd = currentWorkspace?.hostPath ?? null;

  // Workspace loading is owned by the WorkspaceProvider; surface load errors here.
  const setRunError = chat.run.setError;
  useEffect(() => {
    const projectId = currentProjectId;
    if (!projectId || workspaceFor(projectId)) return;
    const controller = new AbortController();
    void apiFetch<ProjectWorkspace>(`/v1/projects/${encodeURIComponent(projectId)}/workspace`, { signal: controller.signal })
      .catch((reason) => {
        if (!controller.signal.aborted) setRunError((reason as Error).message);
      });
    return () => controller.abort();
    // workspaceFor is stable via context; re-run on project switch.
  }, [currentProjectId, setRunError, workspaceFor]);

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

  const combinedError = chat.error ?? sessionNavigation.error;
  const clearCombinedError = () => { chat.clearError(); sessionNavigation.setError(null); };

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
              branches: chat.branches.branches,
              activeBranchId: chat.branches.activeBranchId,
              loading: chat.branches.loading,
              changing: chat.branches.changing,
              disabled: Boolean(chat.run.activeRun),
              onActivate: chat.branches.activateBranch,
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
            selectedSession={chat.history.sessionId}
            activeLeafMessageId={chat.history.activeLeafMessageId}
            activeBranchId={chat.history.activeBranchId}
            branchChanging={chat.branches.changing}
            createBranch={chat.branches.createBranch}
            recovery={chat.run.recovery}
            retrying={chat.run.retrying}
            retryRun={chat.run.retryRun}
            messages={chat.history.messages}
            historyLoading={chat.history.loading}
            historyLoadingOlder={chat.history.loadingOlder}
            historyError={chat.history.error}
            hasMoreHistory={chat.history.hasMore}
            loadOlderHistory={chat.history.loadOlder}
            error={combinedError}
            clearError={clearCombinedError}
            status={chat.run.status}
            input={chat.composer.input}
            setInput={chat.composer.setInput}
            activeRun={chat.run.activeRun}
            activeRunStatus={chat.run.activeRunStatus}
            compacting={chat.run.compacting}
            permissionMode={chat.composer.displayedPermissionMode}
            permissionReady={chat.composer.permissionReady}
            setPermissionMode={chat.composer.setPermissionMode}
            pendingApproval={chat.run.pendingApproval}
            resolvingApproval={chat.run.resolvingApproval}
            decideApproval={chat.run.decideApproval}
            pendingImage={chat.composer.pendingImage}
            clearPendingImage={chat.composer.clearPendingImage}
            uploadImage={chat.composer.uploadImage}
            models={chat.composer.models}
            selectedModelId={chat.composer.selectedModelId}
            setSelectedModelId={chat.composer.setSelectedModelId}
            thinkingEffort={chat.composer.thinkingEffort}
            setThinkingEffort={chat.composer.setThinkingEffort}
            roles={chat.composer.roles}
            selectedRoleId={chat.composer.selectedRoleId}
            setSelectedRoleId={chat.composer.setSelectedRoleId}
            textAttachments={chat.composer.textAttachments}
            removeTextAttachment={chat.composer.removeTextAttachment}
            attachFiles={chat.composer.attachFiles}
            submit={chat.actions.submit}
            steer={chat.actions.steer}
            followUp={chat.actions.followUp}
            cancel={chat.run.cancel}
            pendingFollowUps={chat.run.pendingFollowUps}
            compactSession={chat.composer.compactSession}
            compactionPromptOpen={chat.composer.compaction.open}
            compactionInstructions={chat.composer.compaction.instructions}
            compactionBusy={chat.composer.compaction.busy}
            setCompactionInstructions={chat.composer.compaction.setInstructions}
            confirmCompaction={chat.composer.compaction.confirm}
            cancelCompaction={chat.composer.compaction.cancel}
            promptTemplates={chat.composer.promptPanel.templates}
            panelRoles={chat.composer.promptPanel.roles}
            panelFlows={chat.composer.promptPanel.flows}
            showPromptPanel={chat.composer.promptPanel.show}
            onPromptSelect={chat.composer.promptPanel.onSelect}
            onRoleSelect={chat.composer.promptPanel.onRoleSelect}
            onFlowSelect={chat.composer.promptPanel.onFlowSelect}
            onPromptPanelClose={chat.composer.promptPanel.onClose}
            expanding={chat.composer.promptPanel.expanding}
            expandDiag={chat.composer.promptPanel.expandDiag}
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
          activeRun={chat.run.activeRun}
          status={chat.run.status}
          permissionMode={chat.composer.displayedPermissionMode}
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
          error={chat.run.error}
          onCreate={(name, hostPath) => void confirmCreateProject(name, hostPath)}
          onClose={cancelCreateProject}
        />
      )}
    </>
    </ChildProgressProvider>
  );
}
