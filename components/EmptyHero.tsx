"use client";

import { Bot } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

/**
 * Hero guidance shown in the chat area when no session is selected. Mirrors
 * the DeepSeek Harness conversation hero: the workspace chip is NOT part of
 * this centered stack — it rides directly above the composer card (see
 * HeroProjectChip, rendered by ChatWindow next to the composer) so the chip
 * sits at the input box's top-left, matching dsh. This component only owns
 * the centered brand mark, headline, and CTAs over the soft glow.
 */
export function EmptyHero(props: {
  selectedProject: string | null;
  hasModel: boolean;
  onNewSession: () => void;
  onOpenSettings: () => void;
}) {
  const { selectedProject, hasModel, onNewSession, onOpenSettings } = props;
  const t = useT();

  return (
    <div className="empty-hero" data-testid="empty-hero">
      <div className="empty-hero-glow" aria-hidden="true" />

      {!selectedProject ? (
        <div className="empty-hero-stack">
          <div className="empty-hero-mark" aria-hidden="true">E</div>
          <h1 className="empty-hero-title">{t("empty.project.title")}</h1>
          <p className="empty-hero-body">{t("empty.project.body")}</p>
          {!hasModel && (
            <button type="button" className="empty-hero-settings" onClick={() => onOpenSettings()}>
              {t("empty.noModel")}
            </button>
          )}
        </div>
      ) : (
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
            <button type="button" className="empty-hero-settings" onClick={() => onOpenSettings()}>
              {t("empty.noModel")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
