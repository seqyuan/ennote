"use client";

import { Bot, FolderOpen, Plus } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

/**
 * Hero guidance shown in the chat area when no session is selected. Mirrors
 * the DeepSeek Harness EmptyHero idea (logo + headline + clear CTAs) but keeps
 * Ennote's own brand and scope: the composer stays disabled until a project
 * and session exist, so the hero carries the missing step instead of leaving a
 * bare placeholder.
 */
export function EmptyHero(props: {
  selectedProject: string | null;
  projectCount: number;
  hasModel: boolean;
  onSelectProject: () => void;
  onNewProject: () => void;
  onNewSession: () => void;
  onOpenSettings: () => void;
}) {
  const { selectedProject, projectCount, hasModel, onSelectProject, onNewProject, onNewSession, onOpenSettings } = props;
  const t = useT();

  // No project selected: guide toward choosing or creating a project.
  if (!selectedProject) {
    return (
      <div className="empty-hero" data-testid="empty-hero">
        <div className="empty-hero-glow" aria-hidden="true" />
        <div className="empty-hero-stack">
          <div className="empty-hero-mark" aria-hidden="true">E</div>
          <h1 className="empty-hero-title">{t("empty.project.title")}</h1>
          <p className="empty-hero-body">{t("empty.project.body")}</p>
          <div className="empty-hero-actions">
            {projectCount > 0 && (
              <button type="button" className="empty-hero-primary" onClick={onSelectProject}>
                <FolderOpen size={15} aria-hidden="true" />
                {t("empty.project.select")}
              </button>
            )}
            <button
              type="button"
              className={projectCount > 0 ? "empty-hero-secondary" : "empty-hero-primary"}
              onClick={onNewProject}
            >
              <Plus size={15} aria-hidden="true" />
              {t("empty.project.create")}
            </button>
          </div>
          {!hasModel && (
            <button type="button" className="empty-hero-settings" onClick={onOpenSettings}>
              {t("empty.noModel")}
            </button>
          )}
        </div>
      </div>
    );
  }

  // Project selected but no session: guide toward starting a conversation.
  return (
    <div className="empty-hero" data-testid="empty-hero">
      <div className="empty-hero-glow" aria-hidden="true" />
      <div className="empty-hero-stack">
        <div className="empty-hero-mark" aria-hidden="true">E</div>
        <h1 className="empty-hero-title">{t("empty.session.title")}</h1>
        <p className="empty-hero-body">{t("empty.session.body")}</p>
        <div className="empty-hero-actions">
          <button type="button" className="empty-hero-primary" onClick={onNewSession}>
            <Bot size={15} aria-hidden="true" />
            {t("empty.session.create")}
          </button>
        </div>
        <p className="empty-hero-hint">{t("empty.session.hint")}</p>
        {!hasModel && (
          <button type="button" className="empty-hero-settings" onClick={onOpenSettings}>
            {t("empty.noModel")}
          </button>
        )}
      </div>
    </div>
  );
}
