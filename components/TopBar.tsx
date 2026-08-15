"use client";

import type { RefObject } from "react";
import { AttentionPanel } from "@/components/AttentionPanel";
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
      <button
        ref={navigationTriggerRef}
        className="topbar-sidebar-toggle"
        onClick={onToggleSidebar}
        title={sidebarOpen ? "Hide sidebar" : "Show sidebar"}
        aria-label="Open navigation"
        aria-expanded={sidebarOpen}
        aria-controls="workspace-navigation"
        style={{
          display: "flex", alignItems: "center", justifyContent: "center",
          width: 32, height: 32, padding: 0,
          background: "var(--bg-panel)", border: "1px solid var(--border)", borderRadius: 7,
          color: "var(--text-muted)", cursor: "pointer", flexShrink: 0, transition: "color 0.12s, background 0.12s",
        }}
        onMouseEnter={(e) => { e.currentTarget.style.color = "var(--text)"; e.currentTarget.style.background = "var(--bg-hover)"; }}
        onMouseLeave={(e) => { e.currentTarget.style.color = "var(--text-muted)"; e.currentTarget.style.background = "var(--bg-panel)"; }}
      >
        {sidebarOpen ? (
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <rect x="3" y="3" width="18" height="18" rx="2" /><line x1="9" y1="3" x2="9" y2="21" />
          </svg>
        ) : (
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
            <line x1="3" y1="6" x2="21" y2="6" /><line x1="3" y1="12" x2="21" y2="12" /><line x1="3" y1="18" x2="21" y2="18" />
          </svg>
        )}
      </button>

      {/* Session title area */}
      <div className="topbar-title-area" style={{ display: "flex", alignItems: "center", gap: 7, minWidth: 0, flex: 1 }}>
        <div style={{ minWidth: 0, display: "flex", alignItems: "center", gap: 6 }}>
          {editingTitle && session ? (
            <input
              ref={titleInputRef}
              value={titleDraft}
              onChange={(e) => setTitleDraft(e.target.value)}
              onKeyDown={handleTitleKeyDown}
              onBlur={() => void handleSaveTitle()}
              style={{
                width: "min(360px, 34vw)", height: 28, boxSizing: "border-box",
                border: "1px solid var(--border)", borderRadius: 6,
                background: "var(--bg-panel)", color: "var(--text)",
                padding: "0 8px", fontSize: 13, fontWeight: 500, outline: "none",
              }}
            />
          ) : (
            <>
              <div
                className="topbar-session-title"
                title={topBarSessionTitle}
                style={{
                  minWidth: 0, maxWidth: "min(420px, 36vw)",
                  overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                  color: "var(--text)", fontSize: 14, fontWeight: 600, lineHeight: 1.2,
                }}
              >
                {topBarSessionTitle}
              </div>
              {session && (
                <button
                  type="button"
                  onClick={handleStartTitleEdit}
                  title="Rename session"
                  style={{
                    width: 22, height: 22, display: "flex", alignItems: "center", justifyContent: "center",
                    padding: 0, border: "none", borderRadius: 5,
                    background: "transparent", color: "var(--text-dim)", cursor: "pointer", flexShrink: 0,
                  }}
                  onMouseEnter={(e) => { e.currentTarget.style.color = "var(--text)"; e.currentTarget.style.background = "var(--bg-hover)"; }}
                  onMouseLeave={(e) => { e.currentTarget.style.color = "var(--text-dim)"; e.currentTarget.style.background = "transparent"; }}
                >
                  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <path d="M12 20h9" /><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
                  </svg>
                </button>
              )}
            </>
          )}
        </div>
        {projectName && (
          <>
            <span className="topbar-project-crumb" style={{ color: "var(--text-dim)", fontSize: 12, flexShrink: 0 }}>/</span>
            <span className="topbar-project-crumb" title={projectPath || projectName}
              style={{ minWidth: 0, maxWidth: "min(280px, 24vw)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", color: "var(--text-muted)", fontSize: 12, lineHeight: 1.2 }}>
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
      <button
        type="button"
        onClick={onToggleRightPanel}
        title={rightPanelOpen ? "Hide panel" : "Show panel"}
        style={{
          display: "flex", alignItems: "center", justifyContent: "center",
          width: 32, height: 32, padding: 0,
          background: rightPanelOpen ? "var(--bg-selected)" : "var(--bg-panel)",
          border: "1px solid var(--border)", borderRadius: 7,
          color: rightPanelOpen ? "var(--accent)" : "var(--text-muted)", cursor: "pointer", flexShrink: 0,
          transition: "color 0.12s, background 0.12s",
        }}
        onMouseEnter={(e) => { e.currentTarget.style.color = "var(--text)"; e.currentTarget.style.background = "var(--bg-hover)"; }}
        onMouseLeave={(e) => {
          e.currentTarget.style.color = rightPanelOpen ? "var(--accent)" : "var(--text-muted)";
          e.currentTarget.style.background = rightPanelOpen ? "var(--bg-selected)" : "var(--bg-panel)";
        }}
      >
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <rect x="14" y="3" width="7" height="18" rx="1" /><rect x="3" y="3" width="7" height="18" rx="1" />
        </svg>
      </button>

      {/* Global attention */}
      <AttentionPanel
        projectId={attention.projectId}
        onNavigate={attention.onNavigate}
      />
    </div>
  );
}
