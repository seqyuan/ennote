"use client";

import { ArrowLeft, Menu, X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import { useState, type RefObject } from "react";
import { HeroesSettings } from "@/components/settings/HeroesSettings";
import { GraphSettings } from "@/components/settings/GraphSettings";
import type { ModelProfile } from "@/components/settings/types";
import type { WorkspaceView } from "@/components/workspace-view";

/**
 * Main-area settings view rendered when the sidebar's Heroes / Graphs
 * item is active. Replaces the chat TopBar + ChatWindow + RightPanel
 * region (center+right) while the sidebar stays mounted. Mirrors the
 * header/error/body chrome the standalone /heroes and /graphs pages
 * used to render (same CSS classes, same data-testid) so the editors
 * behave identically inside the shell.
 */
export function SettingsView({
  view,
  models,
  onBackToChat,
  onOpenMobileNav,
  menuTriggerRef,
}: {
  view: Exclude<WorkspaceView, "chat">;
  models: ModelProfile[];
  onBackToChat: () => void;
  onOpenMobileNav: () => void;
  menuTriggerRef: RefObject<HTMLButtonElement | null>;
}) {
  const t = useT();
  const [error, setError] = useState<string | null>(null);
  const title = view === "heroes" ? t("settings.view.heroes") : t("settings.view.graphs");
  const description = view === "heroes" ? t("settings.view.heroesDesc") : t("settings.view.graphsDesc");

  return (
    <div className="settings-view" data-testid={view === "heroes" ? "heroes-page" : "graphs-page"}>
      <button ref={menuTriggerRef} type="button" className="workspace-mobile-menu" onClick={onOpenMobileNav} aria-label={t("settings.view.openNav")}>
        <Menu size={20} />
      </button>

      <header className="workspace-page-header">
        <div>
          <h1>{title}</h1>
          <p>{description}</p>
        </div>
        <button type="button" className="settings-view-back" onClick={onBackToChat}>
          <ArrowLeft size={15} /> {t("settings.view.backToChat")}
        </button>
      </header>

      {error && (
        <div className="error-bar workspace-page-error" role="alert">
          <span>{error}</span>
          <button type="button" onClick={() => setError(null)} aria-label={t("settings.view.dismissError")}>
            <X size={16} />
          </button>
        </div>
      )}

      <div className="workspace-page-body">
        {view === "heroes"
          ? <HeroesSettings models={models} setError={setError} />
          : <GraphSettings setError={setError} />}
      </div>
    </div>
  );
}
