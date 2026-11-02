"use client";

import { ConfirmHost } from "@/components/ui/confirm";
import { ProjectCreateDialog } from "./ProjectCreateDialog";
import { SettingsDialog } from "./settings/SettingsDialog";
import type { ModelProfile, PolicyProfile, ProviderProfile, Session, SettingsTab } from "./settings/types";
import { useWorkspace } from "./WorkspaceProvider";

/**
 * Shell-level dialogs: Settings, the global confirm host, and "new project".
 * Split out of AppShell so the shell stays under the 500-line size guard. The
 * open/close state of both dialogs is owned by WorkspaceProvider, so the
 * component reads it from there instead of taking a bag of hook internals.
 */
interface AppShellDialogsProps {
  settingsOpen: boolean;
  settingsTab?: SettingsTab;
  /** Onboarding-aware close: also records the first-run dismissal. */
  onCloseSettings: () => void;
  providers: ProviderProfile[];
  models: ModelProfile[];
  policies: PolicyProfile[];
  /** Session the contextual settings sections act on; undefined off chat. */
  session?: Session;
  projectId: string | null;
  error: string | null;
  refresh: () => Promise<void>;
  setError: (value: string | null) => void;
  onSessionUpdated: (session: Session) => void;
}

export function AppShellDialogs({
  settingsOpen,
  settingsTab,
  onCloseSettings,
  providers,
  models,
  policies,
  session,
  projectId,
  error,
  refresh,
  setError,
  onSessionUpdated,
}: AppShellDialogsProps) {
  const { createProjectOpen, createProjectBusy, confirmCreateProject, cancelCreateProject } = useWorkspace();

  return (
    <>
      <SettingsDialog
        open={settingsOpen}
        onClose={onCloseSettings}
        initialTab={settingsTab}
        providers={providers}
        models={models}
        policies={policies}
        session={session}
        refresh={refresh}
        error={error}
        setError={setError}
        onSessionUpdated={onSessionUpdated}
        projectId={projectId}
      />

      <ConfirmHost />

      {createProjectOpen && (
        <ProjectCreateDialog
          busy={createProjectBusy}
          onCreate={confirmCreateProject}
          onClose={cancelCreateProject}
        />
      )}
    </>
  );
}
