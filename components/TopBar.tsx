"use client";

import type { RefObject } from "react";
import { Menu, PanelLeft, PanelRight, Pencil } from "lucide-react";
import { AttentionPanel } from "@/components/AttentionPanel";
import { IconButton } from "@/components/ui";
import { BranchControl } from "./BranchControl";
import { ThemeControl } from "./ThemeControl";
import type { useSessionTitle } from "@/hooks/useSessionTitle";
import type { Session } from "@/components/settings/types";
import type { components } from "@/lib/worker-api.gen";

type SessionBranch = components["schemas"]["SessionBranch"];
type AttentionItem = Parameters<NonNullable<React.ComponentProps<typeof AttentionPanel>["onNavigate"]>>[0];

export function TopBar(props: {
  sidebarOpen: boolean;
  onToggleSidebar: () => void;
  navigationTriggerRef: RefObject<HTMLButtonElement | null>;
  session: Session | null | undefined;
  title: ReturnType<typeof useSessionTitle>;
  projectName: string;
  projectPath: string | null;
  branch: {
    branches: SessionBranch[];
    activeBranchId?: string;
    loading: boolean;
    changing: boolean;
    disabled: boolean;
    onActivate: (branchId: string) => void;
  };
  rightPanelOpen: boolean;
  onToggleRightPanel: () => void;
  attention: { projectId?: string; onNavigate: (item: AttentionItem) => void };
}) {
  const {
    sidebarOpen, onToggleSidebar, navigationTriggerRef, session,
    title, projectName, projectPath, branch, rightPanelOpen, onToggleRightPanel, attention,
  } = props;
  const {
    title: topBarSessionTitle, editing: editingTitle, draft: titleDraft, setDraft: setTitleDraft,
    startEdit: handleStartTitleEdit, save: handleSaveTitle, keyDown: handleTitleKeyDown, inputRef: titleInputRef,
  } = title;

  return (
    <div className="app-topbar">
      {/* Sidebar toggle */}
      <IconButton
        ref={navigationTriggerRef}
        label={sidebarOpen ? "Hide navigation" : "Open navigation"}
        title={sidebarOpen ? "Hide sidebar" : "Show sidebar"}
        aria-expanded={sidebarOpen}
        aria-controls="workspace-navigation"
        onClick={onToggleSidebar}
      >
        {sidebarOpen
          ? <PanelLeft size={16} aria-hidden="true" />
          : <Menu size={18} aria-hidden="true" />}
      </IconButton>

      {/* Session title area */}
      <div className="topbar-title-area">
        <div className="topbar-title-group">
          {editingTitle && session ? (
            <input
              ref={titleInputRef}
              className="topbar-title-input"
              value={titleDraft}
              onChange={(e) => setTitleDraft(e.target.value)}
              onKeyDown={handleTitleKeyDown}
              onBlur={() => void handleSaveTitle()}
            />
          ) : (
            <>
              <div className="topbar-session-title" title={topBarSessionTitle}>
                {topBarSessionTitle}
              </div>
              {session && (
                <button
                  type="button"
                  className="topbar-title-edit"
                  onClick={handleStartTitleEdit}
                  title="Rename session"
                  aria-label="Rename session"
                >
                  <Pencil size={12} aria-hidden="true" />
                </button>
              )}
            </>
          )}
        </div>
        {projectName && (
          <>
            <span className="topbar-project-sep">/</span>
            <span className="topbar-project-crumb" title={projectPath || projectName}>
              {projectName}
            </span>
          </>
        )}
      </div>

      {session && (
        <BranchControl
          branches={branch.branches}
          activeBranchId={branch.activeBranchId}
          loading={branch.loading}
          changing={branch.changing}
          disabled={branch.disabled}
          activate={branch.onActivate}
        />
      )}

      <ThemeControl />

      {/* Right panel toggle */}
      <IconButton
        label={rightPanelOpen ? "Hide panel" : "Show panel"}
        title={rightPanelOpen ? "Hide panel" : "Show panel"}
        active={rightPanelOpen}
        onClick={onToggleRightPanel}
      >
        <PanelRight size={16} aria-hidden="true" />
      </IconButton>

      {/* Global attention */}
      <AttentionPanel
        projectId={attention.projectId}
        onNavigate={attention.onNavigate}
      />
    </div>
  );
}
