"use client";

import { Database, FileText, Layers, Plug, Search, Settings2, Shield, Sparkles, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";
import { ContextSettings } from "@/components/settings/ContextSettings";
import { GeneralSettings } from "@/components/settings/GeneralSettings";
import { McpSettings } from "@/components/settings/McpSettings";
import { SkillsSettings } from "@/components/settings/SkillsSettings";
import { ModelsSettings } from "@/components/settings/ModelsSettings";
import { PoliciesSettings } from "@/components/settings/PoliciesSettings";
import { TemplatesSettings } from "@/components/settings/TemplatesSettings";
import { SettingsStatusBadge, SettingsStatusProvider, useSettingsStatus } from "@/components/settings/SettingsStatus";
import type { ModelProfile, PolicyProfile, ProviderProfile, Session, SettingsTab } from "@/components/settings/types";

interface TabItem {
  id: SettingsTab;
  label: string;
  description: string;
  icon: React.ReactNode;
}

function tabIcon(id: SettingsTab) {
  switch (id) {
    case "general": return <Settings2 size={15} aria-hidden="true" />;
    case "models": return <Database size={15} aria-hidden="true" />;
    case "policies": return <Shield size={15} aria-hidden="true" />;
    case "context": return <Layers size={15} aria-hidden="true" />;
    case "templates": return <FileText size={15} aria-hidden="true" />;
    case "mcp": return <Plug size={15} aria-hidden="true" />;
    case "skills": return <Sparkles size={15} aria-hidden="true" />;
  }
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
      <SettingsStatusProvider>
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
      </SettingsStatusProvider>
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
  const t = useT();
  const { markError } = useSettingsStatus();
  const reportError = useCallback((value: string | null) => {
    setError(value);
    if (value) markError(value);
  }, [setError, markError]);
  const [activeTab, setActiveTab] = useState<SettingsTab>(initialTab ?? "general");
  const [query, setQuery] = useState("");
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  useOverlayLayer(true, onClose);

  useEffect(() => {
    const focusDialog = window.requestAnimationFrame(() => closeRef.current?.focus());
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        searchRef.current?.focus();
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
    };
  }, []);

  const tabs: TabItem[] = [
    { id: "general", label: "General", description: "Appearance and theme", icon: tabIcon("general") },
    { id: "models", label: "Models", description: "Providers, API keys, and model profiles", icon: tabIcon("models") },
    { id: "policies", label: "Policies", description: "Tool permission and routing policies", icon: tabIcon("policies") },
    { id: "context", label: "Context & session", description: session ? "Session defaults and compaction" : "Compaction policy defaults", icon: tabIcon("context") },
    { id: "templates", label: "Templates", description: "Slash-command prompt templates", icon: tabIcon("templates") },
    { id: "mcp", label: "MCP", description: "MCP servers and project bindings", icon: tabIcon("mcp") },
    { id: "skills", label: "Skills", description: "Skill catalog, marketplace, and updates", icon: tabIcon("skills") },
  ];

  const normalizedQuery = query.trim().toLowerCase();
  const visibleTabs = normalizedQuery === ""
    ? tabs
    : tabs.filter((tab) => `${tab.label} ${tab.description}`.toLowerCase().includes(normalizedQuery));

  const onSearchChange = (value: string) => {
    setQuery(value);
    const needle = value.trim().toLowerCase();
    if (needle === "") return;
    const matches = tabs.filter((tab) => `${tab.label} ${tab.description}`.toLowerCase().includes(needle));
    if (matches.length > 0 && !matches.some((tab) => tab.id === activeTab)) {
      setActiveTab(matches[0].id);
    }
  };

  return (
    <div
      ref={dialogRef}
      className="settings-dialog-shell"
      role="dialog"
      aria-modal="true"
      aria-label="Settings"
    >
        {/* Body */}
        <div className="settings-dialog-body">
          {/* Tabs (dsh nav rail: section title on top, cells below) */}
          <div className="settings-dialog-tabs" role="tablist" aria-label="Settings sections">
            <div id="settings-dialog-title" className="settings-dialog-navtitle">Settings</div>
            <label className="settings-dialog-search">
              <Search size={14} aria-hidden="true" />
              <input
                ref={searchRef}
                type="text"
                value={query}
                placeholder={t("settings.search.placeholder")}
                aria-label={t("settings.search.aria")}
                onChange={(event) => onSearchChange(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Escape" && query !== "" && !event.nativeEvent.isComposing) {
                    event.preventDefault();
                    event.stopPropagation();
                    setQuery("");
                    return;
                  }
                  if (event.key === "Enter" && visibleTabs.length > 0) {
                    event.preventDefault();
                    setActiveTab(visibleTabs[0].id);
                  }
                }}
              />
              {query !== "" && (
                <button type="button" aria-label={t("settings.search.clear")} onClick={() => setQuery("")}>
                  <X size={12} aria-hidden="true" />
                </button>
              )}
            </label>
            {visibleTabs.length === 0 && <div className="settings-dialog-empty">{t("settings.search.empty").replace("{query}", query.trim())}</div>}
            {visibleTabs.map((tab, index) => {
              const active = activeTab === tab.id;
              return (
                <button
                  key={tab.id}
                  type="button"
                  onClick={() => setActiveTab(tab.id)}
                  onKeyDown={(event) => {
                    let nextIndex: number | null = null;
                    if (event.key === "ArrowRight" || event.key === "ArrowDown") nextIndex = (index + 1) % visibleTabs.length;
                    if (event.key === "ArrowLeft" || event.key === "ArrowUp") nextIndex = (index - 1 + visibleTabs.length) % visibleTabs.length;
                    if (event.key === "Home") nextIndex = 0;
                    if (event.key === "End") nextIndex = visibleTabs.length - 1;
                    if (nextIndex === null) return;
                    event.preventDefault();
                    const nextTab = visibleTabs[nextIndex];
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
                <SettingsStatusBadge />
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
              <div className="settings-tab-scroll">
                <GeneralSettings />
              </div>
            )}
            {activeTab === "models" && (
              <div className="settings-tab-scroll">
                <ModelsSettings providers={providers} models={models} refresh={refresh} setError={reportError} />
              </div>
            )}
            {activeTab === "policies" && (
              <div className="settings-tab-scroll">
                <PoliciesSettings policies={policies} refresh={refresh} setError={reportError} />
              </div>
            )}
            {activeTab === "context" && (
              <div className="settings-tab-scroll">
                <ContextSettings
                  policies={policies} models={models} session={session}
                  refresh={refresh} setError={reportError}
                  onSessionUpdated={onSessionUpdated}
                />
              </div>
            )}
            {activeTab === "templates" && (
              <div className="settings-tab-scroll">
                <TemplatesSettings projectId={projectId} setError={reportError} />
              </div>
            )}
            {activeTab === "mcp" && (
              <div className="settings-tab-scroll">
                <McpSettings projectId={projectId} setError={reportError} />
              </div>
            )}
            {activeTab === "skills" && (
              <div className="settings-tab-scroll">
                <SkillsSettings projectId={projectId} setError={reportError} />
              </div>
            )}
            </div>
          </div>
        </div>
      </div>
  );
}
