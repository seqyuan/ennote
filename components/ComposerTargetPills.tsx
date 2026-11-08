"use client";

import { Bot, Sparkles, Workflow, X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

export interface PillHero {
  id: string;
  handle: string;
  name: string;
}

/**
 * The run's addressed targets, rendered where attachments live.
 *
 * A Hero, a graph and the turn's Skills are all "carried by this turn", so they
 * share the attachment shelf instead of competing with the permission, model and
 * context controls in the toolbar. The Hero stays until the session changes (it
 * answers "who am I talking to"); the graph and Skills are one-shot and clear on
 * send, so a second turn never silently inherits instructions the user did not
 * re-choose.
 */
export function ComposerTargetPills({
  hero, graph, skills, onClearHero, onClearGraph, onRemoveSkill,
}: {
  hero: PillHero | null;
  graph: { name: string; version?: number } | null;
  skills: { id: string; label: string }[];
  onClearHero: () => void;
  onClearGraph: () => void;
  onRemoveSkill: (id: string) => void;
}) {
  const t = useT();
  if (!hero && !graph && skills.length === 0) return null;
  return (
    <div className="composer-target-pills" aria-label={t("composer.targets")}>
      {hero && (
        <span className="composer-pill is-hero">
          <Bot size={12} aria-hidden="true" />
          <span className="composer-pill-label">@{hero.handle}</span>
          <button type="button" aria-label={`${t("composer.removeHero")} ${hero.name}`} onClick={onClearHero}>
            <X size={10} aria-hidden="true" />
          </button>
        </span>
      )}
      {graph && (
        <span className="composer-pill is-graph">
          <Workflow size={12} aria-hidden="true" />
          <span className="composer-pill-label">
            {graph.name}{graph.version ? `@${graph.version}` : ""}
          </span>
          <button type="button" aria-label={`${t("composer.removeGraph")} ${graph.name}`} onClick={onClearGraph}>
            <X size={10} aria-hidden="true" />
          </button>
        </span>
      )}
      {skills.map((skill) => (
        <span className="composer-pill is-skill" key={skill.id}>
          <Sparkles size={12} aria-hidden="true" />
          <span className="composer-pill-label">/{skill.label}</span>
          <button type="button" aria-label={`${t("composer.removeSkill")} ${skill.label}`} onClick={() => onRemoveSkill(skill.id)}>
            <X size={10} aria-hidden="true" />
          </button>
        </span>
      ))}
    </div>
  );
}
