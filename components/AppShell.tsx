"use client";

import { useState, useCallback, useRef, useEffect } from "react";
import { SessionSidebar } from "./SessionSidebar";
import { SidebarExpandFab } from "./SidebarExpandFab";
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
import { ChildProgressProvider } from "@/hooks/useChildProgress";
import { useWorkspace } from "./WorkspaceProvider";
import { useRunningSessionIds } from "@/hooks/useRunningSessionIds";
import { useSettingsProfiles } from "@/hooks/useSettingsProfiles";
import { usePromptTemplates } from "@/hooks/usePromptTemplates";
import { useChatController } from "@/hooks/useChatController";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { useProjectSelector } from "@/hooks/useProjectSelector";
import { SettingsView } from "./SettingsView";
import type { WorkspaceView } from "./workspace-view";
import { apiFetch } from "@/lib/worker-api.client";
import type { Session } from "@/components/settings/types";
import type { components } from "@/lib/worker-api.gen";

type ProjectWorkspace = components["schemas"]["ProjectWorkspace"];

export function AppShell({ initialView = "chat" }: { initialView?: WorkspaceView }) {
  const isMobile = useMediaQuery("(max-width: 640px)");
  const {
    projects, selectedProject, switchProject: workspaceSwitchProject,
    createProjectOpen, openCreateProject, confirmCreateProject, cancelCreateProject, createProjectBusy,
    settingsOpen, openSettings: workspaceOpenSettings, closeSettings,
    workspaceFor, togglePinProject, pinnedProjectIds, renameProject, deleteProject,
  } = useWorkspace();

  // Session selection stays in the chat shell; the composer reset that used to
  // live in selectSession is now declarative inside useChatController.
  const [selectedSession, setSelectedSessionState] = useState<string | null>(null);

  // Main-area view (chat / roles / graphs). Switching only swaps the
  // center+right region; the sidebar stays mounted. The URL is kept in
  // sync (?view=roles) via history.replaceState so deep links, refresh,
  // and browser back/forward preserve the view.
  const [view, setView] = useState<WorkspaceView>(initialView ?? "chat");
  const changeView = useCallback((next: WorkspaceView) => {
    setView(next);
    if (typeof window !== "undefined") {
      window.history.replaceState(null, "", next === "chat" ? "/" : `/?view=${next}`);
    }
  }, []);
  useEffect(() => {
    const applyFromUrl = () => {
      if (typeof window === "undefined") return;
      const candidate = new URLSearchParams(window.location.search).get("view");
      if (candidate === "roles" || candidate === "graphs" || candidate === "chat") {
        setView(candidate);
      }
    };
    applyFromUrl();
    window.addEventListener("popstate", applyFromUrl);
    return () => window.removeEventListener("popstate", applyFromUrl);
  }, []);

  const selectSession = useCallback((sessionId: string | null) => {
    setSelectedSessionState(sessionId);
  }, []);

  // Resizable panels
  const sidebarResize = useResizable({ initialWidth: 280, minWidth: 264, maxWidth: 420, storageKey: "--ennote-sidebar-width" });
  const rightPanelResize = useResizable({ initialWidth: 420, minWidth: 280, maxWidth: 1000, storageKey: "--ennote-right-panel-width", direction: "left" });

  // Project selector dropdown is owned here for the sidebar; the middle
  // channel (hero chip + composer) keeps its own instance inside ChatWindow.
  const projectSelector = useProjectSelector();

  // Right panel layout state (file tabs live in useFileTabs)
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [rightPanelOpen, setRightPanelOpen] = useState(false);
  const [previewFile, setPreviewFile] = useState<{ projectId: string; path: string; name: string } | null>(null);

  // Top bar
  const navigationTriggerRef = useRef<HTMLButtonElement>(null);
  // Settings view's mobile menu button (focus target when the chat TopBar is
  // not rendered because a Roles/Graphs view is active).
  const settingsMenuTriggerRef = useRef<HTMLButtonElement>(null);

  const closeMobileNavigation = useCallback(() => {
    setSidebarOpen(false);
    window.requestAnimationFrame(() => {
      (navigationTriggerRef.current ?? settingsMenuTriggerRef.current)?.focus();
    });
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
      )).filter((element) => element.getClientRects().length > 0);
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
  // hooks live inside useChatController and surface as composed views).
  const sessionNavigation = useProjectSessions(selectedProject);
  const sidebarGroups = useSidebarProjectGroups(projects, pinnedProjectIds, selectedProject);
  const settings = useSettingsProfiles();
  const replaceSession = sessionNavigation.replaceSession;
  const selectedSessionRecord = sessionNavigation.activeSessions.find(s => s.id === selectedSession);

  const promptCatalog = usePromptTemplates(selectedProject);

  // Chat domain: composer state, session data hooks and all chat actions live
  // in the controller; AppShell reads only the composed views plus the run id
  // for sidebar run indicators.
  const chat = useChatController({
    selectedSession,
    sessionRecord: selectedSessionRecord,
    selectedProject,
    promptCatalog,
    settings,
    replaceSession,
  });

  const runningSessionIds = useRunningSessionIds(
    sidebarGroups.groups.flatMap((group) => group.sessions.map((session) => session.id)),
    chat.run.activeRun ? selectedSession : null,
  );

  const refreshSettings = settings.refresh;
  const openSettings = useCallback(() => {
    workspaceOpenSettings();
    void refreshSettings();
  }, [refreshSettings, workspaceOpenSettings]);

  // Feature: first-run guidance — when no provider profile exists, open the
  // Models settings tab once so the user can configure a provider instead of
  // staring at an inert composer. Mirrors DeepSeek Harness' provider onboarding.
  const autoOpenedSettings = useRef(false);
  useEffect(() => {
    if (autoOpenedSettings.current || settings.loading || settings.error) return;
    if (settings.providers.length > 0) return;
    autoOpenedSettings.current = true;
    openSettings();
  }, [settings.loading, settings.error, settings.providers.length, openSettings]);

  const switchProject = useCallback((projectId: string) => {
    workspaceSwitchProject(projectId);
    changeView("chat");
    selectSession(null);
    sessionNavigation.setView("active");
    sessionNavigation.setQuery("");
    if (isMobile) closeMobileNavigation();
  }, [changeView, closeMobileNavigation, isMobile, selectSession, sessionNavigation, workspaceSwitchProject]);

  const createSessionIn = useCallback(async (projectId: string) => {
    try {
      const session = await apiFetch<Session>(`/v1/projects/${encodeURIComponent(projectId)}/sessions`, {
        method: "POST", body: JSON.stringify({ title: `Chat ${new Date().toLocaleTimeString()}` }),
      });
      chat.run.setError(null);
      sessionNavigation.replaceSession(session);
      selectSession(session.id);
      changeView("chat");
      await sessionNavigation.refresh();
      sidebarGroups.refresh(projectId);
    } catch (reason) {
      chat.run.setError((reason as Error).message);
    }
  }, [changeView, selectSession, sessionNavigation, sidebarGroups, chat.run]);

  const createSession = useCallback(async () => {
    if (selectedProject) await createSessionIn(selectedProject);
  }, [createSessionIn, selectedProject]);

  const renameSession = useCallback(async (session: Session, title: string) => {
    const updated = await apiFetch<Session>(`/v1/sessions/${encodeURIComponent(session.id)}`, {
      method: "PATCH", body: JSON.stringify({ title }),
    });
    sessionNavigation.replaceSession(updated);
    sidebarGroups.refresh(session.projectId);
  }, [sessionNavigation, sidebarGroups]);

  const handleRenameProject = useCallback(async (projectId: string, name: string) => {
    await renameProject(projectId, name);
    sidebarGroups.refresh(projectId);
  }, [renameProject, sidebarGroups]);

  const handleDeleteProject = useCallback(async (projectId: string) => {
    await deleteProject(projectId);
  }, [deleteProject]);

  const switchSession = useCallback((sessionId: string) => {
    closeSettings();
    changeView("chat");
    selectSession(sessionId);
    apiFetch<Session>(`/v1/sessions/${encodeURIComponent(sessionId)}`).then(sessionNavigation.replaceSession).catch(() => {});
    if (isMobile) closeMobileNavigation();
  }, [changeView, closeMobileNavigation, closeSettings, isMobile, selectSession, sessionNavigation]);

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
      renameProject={handleRenameProject}
      deleteProject={handleDeleteProject}
      collapsed={sidebarGroups.collapsed}
      toggleCollapsed={sidebarGroups.toggleCollapsed}
      archived={sidebarGroups.archived}
      openArchived={sidebarGroups.openArchived}
      createProject={openCreateProject}
      createSession={createSession}
      createSessionIn={createSessionIn}
      renameSession={renameSession}
      switchProject={switchProject}
      switchSession={switchSession}
      archiveSession={session => void archiveSession(session)}
      restoreSession={session => void restoreSession(session)}
      openSettings={openSettings}
      closeNavigation={closeMobileNavigation}
      runningSessionIds={runningSessionIds}
      onToggleSidebar={() => setSidebarOpen((v) => !v)}
      projectSelector={projectSelector}
      view={view}
      setView={changeView}
    />
  );

  const combinedError = chat.error ?? sessionNavigation.error;
  const clearCombinedError = () => { chat.clearError(); sessionNavigation.setError(null); };

  return (
    <ChildProgressProvider>
    <>
      <div className={`app-shell${!sidebarOpen && !isMobile ? " sidebar-collapsed" : ""}`} data-testid="ennote-shell">
        {/* Floating top-left expand button: the desktop sidebar fully collapses
            to 0 width (no rail), so a fixed button in the corner reopens it.
            Mobile keeps the topbar hamburger / settings-view menu button. */}
        {!sidebarOpen && !isMobile && <SidebarExpandFab onClick={() => setSidebarOpen(true)} />}

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
            width: isMobile ? undefined : sidebarOpen ? sidebarResize.width : 56,
            minWidth: isMobile ? undefined : sidebarOpen ? sidebarResize.width : 56,
            transition: sidebarOpen || sidebarResize.isResizing ? "none" : undefined,
          }}
        >
          {sidebar}
        </div>

        {sidebarOpen && !isMobile && (
          <ResizeHandle
            side="right"
            ariaLabel="Resize sidebar"
            value={sidebarResize.width}
            min={264}
            max={420}
            onResizeStart={sidebarResize.beginResize}
            onResize={sidebarResize.resizeBy}
            onResizeEnd={sidebarResize.endResize}
          />
        )}

        {/* Center: chat (or Roles/Graphs settings view) */}
        <div className="workspace-content" inert={isMobile && sidebarOpen} style={{ flex: 1, display: "flex", flexDirection: "column", overflow: "hidden", minWidth: 0 }}>
          {view === "chat" ? (
            <>
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
                history={chat.history}
                run={chat.run}
                branches={chat.branches}
                composer={chat.composer}
                actions={chat.actions}
                error={combinedError}
                clearError={clearCombinedError}
                projects={projects}
                selectedProject={selectedProject}
                hasModel={settings.models.length > 0}
                pinnedProjectIds={pinnedProjectIds}
                togglePinProject={togglePinProject}
                onSwitchProject={switchProject}
                onNewProject={openCreateProject}
                onNewSession={() => void createSession()}
                onOpenSettings={openSettings}
              />
            </>
          ) : (
            <SettingsView
              view={view}
              models={settings.models}
              providers={settings.providers}
              onBackToChat={() => changeView("chat")}
              onOpenMobileNav={() => setSidebarOpen(true)}
              menuTriggerRef={settingsMenuTriggerRef}
            />
          )}
        </div>

        {/* Right panel resize handle (chat view only) */}
        {view === "chat" && rightPanelOpen && (
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

        {/* Right panel (chat view only) */}
        {view === "chat" && (
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
        )}

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
          onCreate={confirmCreateProject}
          onClose={cancelCreateProject}
        />
      )}
    </>
    </ChildProgressProvider>
  );
}
