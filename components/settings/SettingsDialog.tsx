"use client";

import { X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ContextSettings } from "@/components/settings/ContextSettings";
import { GeneralSettings } from "@/components/settings/GeneralSettings";
import { McpSettings } from "@/components/settings/McpSettings";
import { SkillsSettings } from "@/components/settings/SkillsSettings";
import { ModelsSettings } from "@/components/settings/ModelsSettings";
import { PoliciesSettings } from "@/components/settings/PoliciesSettings";
import { TemplatesSettings } from "@/components/settings/TemplatesSettings";
import type { ModelProfile, PolicyProfile, ProviderProfile, Session, SettingsTab } from "@/components/settings/types";

interface TabItem {
  id: SettingsTab;
  label: string;
  description: string;
  icon: React.ReactNode;
}

function tabIcon(id: SettingsTab) {
  if (id === "general") {
    return (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <line x1="4" y1="21" x2="4" y2="14" />
        <line x1="4" y1="10" x2="4" y2="3" />
        <line x1="12" y1="21" x2="12" y2="12" />
        <line x1="12" y1="8" x2="12" y2="3" />
        <line x1="20" y1="21" x2="20" y2="16" />
        <line x1="20" y1="12" x2="20" y2="3" />
        <line x1="1" y1="14" x2="7" y2="14" />
        <line x1="9" y1="8" x2="15" y2="8" />
        <line x1="17" y1="16" x2="23" y2="16" />
      </svg>
    );
  }
  if (id === "models") {
    return (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="4" y="4" width="16" height="16" rx="2" />
        <rect x="9" y="9" width="6" height="6" />
        <line x1="9" y1="1" x2="9" y2="4" />
        <line x1="15" y1="1" x2="15" y2="4" />
        <line x1="9" y1="20" x2="9" y2="23" />
        <line x1="15" y1="20" x2="15" y2="23" />
      </svg>
    );
  }
  if (id === "policies") {
    return (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
      </svg>
    );
  }
  if (id === "mcp") {
    return (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="2" y="2" width="20" height="20" rx="2" />
        <line x1="2" y1="12" x2="22" y2="12" />
        <line x1="12" y1="2" x2="12" y2="22" />
      </svg>
    );
  }
  if (id === "skills") {
    return (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 3l1.9 4.6L18.5 9l-4.6 1.9L12 15.5l-1.9-4.6L5.5 9l4.6-1.4z" />
        <path d="M19 15l.9 2.1L22 18l-2.1.9L19 21l-.9-2.1L16 18l2.1-.9z" />
      </svg>
    );
  }
  return (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M21 12a9 9 0 0 1-9 9m9-9a9 9 0 0 0-9-9m9 9H3m9 9a9 9 0 0 1-9-9m9 9c1.66 0 3-4.03 3-9s-1.34-9-3-9m0 18c-1.66 0-3-4.03-3-9s1.34-9 3-9m-9 9a9 9 0 0 1 9-9" />
    </svg>
  );
}

export function SettingsDialog({ open, onClose, initialTab, providers, models, policies, session, refresh, error, setError,
  onSessionUpdated, projectId }: {
  open: boolean;
  onClose: () => void;
  /** Section to land on when the dialog opens; defaults to the first tab
   *  (dsh: the trigger reopens on the first nav row; onboarding targets a
   *  specific section explicitly). */
  initialTab?: SettingsTab;
  providers: ProviderProfile[];
  models: ModelProfile[];
  policies: PolicyProfile[];
  session?: Session;
  refresh: () => Promise<void>;
  error: string | null;
  setError: (value: string | null) => void;
  onSessionUpdated: (session: Session) => void;
  projectId: string | null;
}) {
  if (!open) return null;
  return (
    <div
      className="settings-dialog-backdrop"
      onClick={(event) => { if (event.target === event.currentTarget) onClose(); }}
    >
      {/* Keyed by the opening section: the body remounts on every open, so
          the active tab resets exactly like dsh (close clears the section;
          the trigger reopens on the first nav row, onboarding re-targets). */}
      <SettingsBody
        key={initialTab ?? "general"}
        initialTab={initialTab}
        onClose={onClose}
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
    </div>
  );
}

function SettingsBody({ onClose, initialTab, providers, models, policies, session, refresh, error, setError,
  onSessionUpdated, projectId }: {
  onClose: () => void;
  initialTab?: SettingsTab;
  providers: ProviderProfile[];
  models: ModelProfile[];
  policies: PolicyProfile[];
  session?: Session;
  refresh: () => Promise<void>;
  error: string | null;
  setError: (value: string | null) => void;
  onSessionUpdated: (session: Session) => void;
  projectId: string | null;
}) {
  const [activeTab, setActiveTab] = useState<SettingsTab>(initialTab ?? "general");
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusDialog = window.requestAnimationFrame(() => closeRef.current?.focus());
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== "Tab" || !dialogRef.current) return;
      const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(
        'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [href], [tabindex]:not([tabindex="-1"])',
      ));
      if (focusable.length === 0) return;
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
    return () => {
      window.cancelAnimationFrame(focusDialog);
      window.removeEventListener("keydown", onKeyDown);
      previousFocus?.focus();
    };
  }, [onClose]);

  const tabs: TabItem[] = [
    { id: "general", label: "General", description: "Appearance and theme", icon: tabIcon("general") },
    { id: "models", label: "Models", description: "Providers, API keys, and model profiles", icon: tabIcon("models") },
    { id: "policies", label: "Policies", description: "Tool permission and routing policies", icon: tabIcon("policies") },
    { id: "context", label: "Context & session", description: session ? "Session defaults and compaction" : "Compaction policy defaults", icon: tabIcon("context") },
    { id: "templates", label: "Templates", description: "Slash-command prompt templates", icon: tabIcon("templates") },
    { id: "mcp", label: "MCP", description: "MCP servers and project bindings", icon: tabIcon("mcp") },
    { id: "skills", label: "Skills", description: "Skill catalog, marketplace, and updates", icon: tabIcon("skills") },
  ];

  return (
    <div
      ref={dialogRef}
      className="settings-dialog-shell"
      role="dialog"
      aria-modal="true"
      aria-labelledby="settings-dialog-title settings-dialog-current"
    >
        {/* Body */}
        <div className="settings-dialog-body">
          {/* Tabs (dsh nav rail: section title on top, cells below) */}
          <div className="settings-dialog-tabs" role="tablist" aria-label="Settings sections">
            <div id="settings-dialog-title" className="settings-dialog-navtitle">Settings</div>
            {tabs.map((tab, index) => {
              const active = activeTab === tab.id;
              return (
                <button
                  key={tab.id}
                  type="button"
                  onClick={() => setActiveTab(tab.id)}
                  onKeyDown={(event) => {
                    let nextIndex: number | null = null;
                    if (event.key === "ArrowRight" || event.key === "ArrowDown") nextIndex = (index + 1) % tabs.length;
                    if (event.key === "ArrowLeft" || event.key === "ArrowUp") nextIndex = (index - 1 + tabs.length) % tabs.length;
                    if (event.key === "Home") nextIndex = 0;
                    if (event.key === "End") nextIndex = tabs.length - 1;
                    if (nextIndex === null) return;
                    event.preventDefault();
                    const nextTab = tabs[nextIndex];
                    setActiveTab(nextTab.id);
                    window.requestAnimationFrame(() => document.getElementById(`settings-tab-${nextTab.id}`)?.focus());
                  }}
                  className={`settings-dialog-tab ${active ? "active" : ""}`}
                  role="tab"
                  id={`settings-tab-${tab.id}`}
                  aria-selected={active}
                  aria-controls={`settings-panel-${tab.id}`}
                >
                  <span className="settings-tab-icon">
                    {tab.icon}
                  </span>
                  <span className="settings-tab-label">{tab.label}</span>
                </button>
              );
            })}
          </div>

          {/* Panel content (dsh: header row with close, options below) */}
          <div className="settings-dialog-content">
            <div className="settings-dialog-header">
              <div className="settings-dialog-actions">
                {/* Mobile-only current-section title; the desktop nav rail
                    keeps its own "Settings" title (dsh). Shown in the top
                    bar on ≤640px, hidden on desktop. */}
                <span id="settings-dialog-current" className="settings-dialog-current">
                  {tabs.find((tab) => tab.id === activeTab)?.label}
                </span>
              </div>
              <button ref={closeRef} type="button" className="settings-dialog-close" title="Close settings" aria-label="Close settings" onClick={onClose}>
                <X size={14} aria-hidden="true" />
              </button>
            </div>
            {error && (
              <div className="error-bar" role="alert">
                <span>{error}</span>
                <button type="button" onClick={() => setError(null)} aria-label="Dismiss error">
                  <X size={15} aria-hidden="true" />
                </button>
              </div>
            )}
            <div
              className="settings-dialog-panel"
              role="tabpanel"
              id={`settings-panel-${activeTab}`}
              aria-labelledby={`settings-tab-${activeTab}`}
            >
            {activeTab === "general" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <GeneralSettings />
              </div>
            )}
            {activeTab === "models" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <ModelsSettings providers={providers} models={models} refresh={refresh} setError={setError} />
              </div>
            )}
            {activeTab === "policies" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <PoliciesSettings policies={policies} refresh={refresh} setError={setError} />
              </div>
            )}
            {activeTab === "context" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <ContextSettings
                  policies={policies} models={models} session={session}
                  refresh={refresh} setError={setError}
                  onSessionUpdated={onSessionUpdated}
                />
              </div>
            )}
            {activeTab === "templates" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <TemplatesSettings projectId={projectId} setError={setError} />
              </div>
            )}
            {activeTab === "mcp" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <McpSettings projectId={projectId} setError={setError} />
              </div>
            )}
            {activeTab === "skills" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <SkillsSettings projectId={projectId} setError={setError} />
              </div>
            )}
            </div>
          </div>
        </div>
      </div>
  );
}
