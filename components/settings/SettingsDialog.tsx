"use client";

import { Bot, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ContextSettings } from "@/components/settings/ContextSettings";
import { McpSettings } from "@/components/settings/McpSettings";
import { AgentFlowSettings } from "@/components/settings/AgentFlowSettings";
import { ModelsSettings } from "@/components/settings/ModelsSettings";
import { PoliciesSettings } from "@/components/settings/PoliciesSettings";
import { ProvidersSettings } from "@/components/settings/ProvidersSettings";
import { RolesSettings } from "@/components/settings/RolesSettings";
import { TemplatesSettings } from "@/components/settings/TemplatesSettings";
import type { ModelProfile, PolicyProfile, ProviderProfile, Session, SettingsTab } from "@/components/settings/types";

interface TabItem {
  id: SettingsTab;
  label: string;
  description: string;
  icon: React.ReactNode;
}

function SettingsIcon() {
  return (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.38a2 2 0 0 0-.73-2.73l-.15-.09a2 2 0 0 1-1-1.74v-.51a2 2 0 0 1 1-1.72l.15-.1a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2Z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

function tabIcon(id: SettingsTab) {
  if (id === "providers") {
    return (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="2" y="2" width="20" height="8" rx="2" ry="2" />
        <rect x="2" y="14" width="20" height="8" rx="2" ry="2" />
        <line x1="6" y1="6" x2="6.01" y2="6" />
        <line x1="6" y1="18" x2="6.01" y2="18" />
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
  if (id === "roles") {
    return <Bot size={15} aria-hidden="true" />;
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
  return (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M21 12a9 9 0 0 1-9 9m9-9a9 9 0 0 0-9-9m9 9H3m9 9a9 9 0 0 1-9-9m9 9c1.66 0 3-4.03 3-9s-1.34-9-3-9m0 18c-1.66 0-3-4.03-3-9s1.34-9 3-9m-9 9a9 9 0 0 1 9-9" />
    </svg>
  );
}

export function SettingsDialog({ open, onClose, providers, models, policies, session, refresh, error, setError,
  onSessionUpdated, projectId }: {
  open: boolean;
  onClose: () => void;
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
  const [activeTab, setActiveTab] = useState<SettingsTab>("providers");
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusDialog = window.requestAnimationFrame(() => dialogRef.current?.querySelector<HTMLElement>(".settings-dialog-tab")?.focus());
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
  }, [onClose, open]);

  if (!open) return null;

  const tabs: TabItem[] = [
    { id: "providers", label: "Providers", description: "API connection profiles and credentials", icon: tabIcon("providers") },
    { id: "models", label: "Models", description: "Configure and assign model profiles", icon: tabIcon("models") },
    { id: "roles", label: "Roles", description: "Addressable identities and immutable versions", icon: tabIcon("roles") },
    { id: "policies", label: "Policies", description: "Tool permission and routing policies", icon: tabIcon("policies") },
    { id: "context", label: "Context & session", description: session ? "Session defaults and compaction" : "Compaction policy defaults", icon: tabIcon("context") },
    { id: "templates", label: "Templates", description: "Slash-command prompt templates", icon: tabIcon("templates") },
    { id: "mcp", label: "MCP", description: "MCP servers and project bindings", icon: tabIcon("mcp") },
    { id: "flows", label: "Flows", description: "Agent Flow tasks, bindings, and runs", icon: tabIcon("flows") },
  ];

  const activeItem = tabs.find((tab) => tab.id === activeTab) ?? tabs[0];

  return (
    <div
      className="settings-dialog-backdrop"
      onClick={(event) => { if (event.target === event.currentTarget) onClose(); }}
    >
      <div
        ref={dialogRef}
        className="settings-dialog-shell"
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-dialog-title"
      >
        {/* Header */}
        <div className="settings-dialog-header">
          <div style={{ display: "flex", alignItems: "center", gap: 9, minWidth: 0 }}>
            <span style={{ color: "var(--text-muted)", display: "flex", alignItems: "center", justifyContent: "center" }}>
              <SettingsIcon />
            </span>
            <div style={{ minWidth: 0 }}>
              <div id="settings-dialog-title" style={{ fontSize: 15, fontWeight: 700, color: "var(--text)", lineHeight: 1.25 }}>Settings</div>
              <div style={{ fontSize: 11, color: "var(--text-dim)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                {activeItem.label} · {activeItem.description}
              </div>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            title="Close settings"
            aria-label="Close settings"
            style={{
              width: 30, height: 30,
              display: "flex", alignItems: "center", justifyContent: "center",
              border: "none", borderRadius: 6,
              background: "transparent", color: "var(--text-muted)", cursor: "pointer",
              fontSize: 20, lineHeight: 1,
            }}
            onMouseEnter={(e) => { e.currentTarget.style.background = "var(--bg-hover)"; e.currentTarget.style.color = "var(--text)"; }}
            onMouseLeave={(e) => { e.currentTarget.style.background = "transparent"; e.currentTarget.style.color = "var(--text-muted)"; }}
          >
            <X size={17} aria-hidden="true" />
          </button>
        </div>

        {/* Error bar */}
        {error && (
          <div className="error-bar" role="alert">
            <span>{error}</span>
            <button type="button" onClick={() => setError(null)} aria-label="Dismiss error">
              <X size={15} aria-hidden="true" />
            </button>
          </div>
        )}

        {/* Body */}
        <div className="settings-dialog-body">
          {/* Tabs */}
          <div className="settings-dialog-tabs" role="tablist" aria-label="Settings sections">
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
                  className="settings-dialog-tab"
                  role="tab"
                  id={`settings-tab-${tab.id}`}
                  aria-selected={active}
                  aria-controls={`settings-panel-${tab.id}`}
                  style={{
                    background: active ? "var(--bg-selected)" : "transparent",
                    color: active ? "var(--text)" : "var(--text-muted)",
                    cursor: "pointer",
                  }}
                  onMouseEnter={(e) => { if (!active) e.currentTarget.style.background = "var(--bg-hover)"; }}
                  onMouseLeave={(e) => { if (!active) e.currentTarget.style.background = "transparent"; }}
                >
                  <span style={{ flexShrink: 0, color: active ? "var(--accent)" : "currentColor", display: "flex" }}>
                    {tab.icon}
                  </span>
                  <span style={{ minWidth: 0, flex: 1 }}>
                    <span style={{ display: "block", fontSize: 12, fontWeight: active ? 700 : 600, lineHeight: 1.25 }}>
                      {tab.label}
                    </span>
                    <span style={{
                      display: "block", marginTop: 2, fontSize: 10,
                      color: "var(--text-dim)", lineHeight: 1.25,
                      overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                    }}>
                      {tab.description}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>

          {/* Panel content */}
          <div
            className="settings-dialog-panel"
            role="tabpanel"
            id={`settings-panel-${activeTab}`}
            aria-labelledby={`settings-tab-${activeTab}`}
          >
            {activeTab === "providers" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <ProvidersSettings providers={providers} models={models} refresh={refresh} setError={setError} />
              </div>
            )}
            {activeTab === "models" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <ModelsSettings providers={providers} models={models} refresh={refresh} setError={setError} />
              </div>
            )}
            {activeTab === "roles" && (
              <RolesSettings projectId={projectId} models={models} setError={setError} />
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
            {activeTab === "flows" && (
              <div style={{ height: "100%", overflowY: "auto" }}>
                <AgentFlowSettings projectId={projectId} setError={setError} />
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
