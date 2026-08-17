"use client";

import { ArrowLeft, Menu, X } from "lucide-react";
import { useState, type RefObject } from "react";
import { RolesSettings } from "@/components/settings/RolesSettings";
import { AgentFlowSettings } from "@/components/settings/AgentFlowSettings";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";
import type { WorkspaceView } from "@/components/workspace-view";

/**
 * Main-area settings view rendered when the sidebar's Roles / Graphs
 * item is active. Replaces the chat TopBar + ChatWindow + RightPanel
 * region (center+right) while the sidebar stays mounted. Mirrors the
 * header/error/body chrome the standalone /roles and /graphs pages
 * used to render (same CSS classes, same data-testid) so the editors
 * behave identically inside the shell.
 */
export function SettingsView({
  view,
  models,
  providers,
  onBackToChat,
  onOpenMobileNav,
  menuTriggerRef,
}: {
  view: Exclude<WorkspaceView, "chat">;
  models: ModelProfile[];
  providers: ProviderProfile[];
  onBackToChat: () => void;
  onOpenMobileNav: () => void;
  menuTriggerRef: RefObject<HTMLButtonElement | null>;
}) {
  const [error, setError] = useState<string | null>(null);
  const title = view === "roles" ? "Roles" : "Graphs";
  const description = view === "roles"
    ? "Global reusable execution presets for Chat and Graph Tasks."
    : "Global Task graphs. Every published Graph can run in any Session.";

  return (
    <div className="settings-view" data-testid={view === "roles" ? "roles-page" : "graphs-page"}>
      <button ref={menuTriggerRef} type="button" className="workspace-mobile-menu" onClick={onOpenMobileNav} aria-label="Open navigation">
        <Menu size={20} />
      </button>

      <header className="workspace-page-header">
        <div>
          <h1>{title}</h1>
          <p>{description}</p>
        </div>
        <button type="button" className="settings-view-back" onClick={onBackToChat}>
          <ArrowLeft size={15} /> Chat
        </button>
      </header>

      {error && (
        <div className="error-bar workspace-page-error" role="alert">
          <span>{error}</span>
          <button type="button" onClick={() => setError(null)} aria-label="Dismiss error">
            <X size={16} />
          </button>
        </div>
      )}

      <div className="workspace-page-body">
        {view === "roles"
          ? <RolesSettings models={models} providers={providers} setError={setError} />
          : <AgentFlowSettings setError={setError} />}
      </div>
    </div>
  );
}
