"use client";

import { Bot, FileText, ImagePlus, Minimize2, Paperclip, Sparkles, Workflow } from "lucide-react";
import { useCallback, useMemo, useState, type ChangeEvent } from "react";
import { useT } from "@/components/LocaleProvider";
import type { PickerItem, PickerSection } from "@/components/ComposerPicker";
import { detectComposerTrigger, type TriggerKind } from "@/lib/composer-trigger";

export interface ComposerPickerData {
  templates: { name: string; description: string; argumentHint: string; source: string; editable: boolean }[];
  heroes: { id: string; handle: string; name: string; description?: string }[];
  graphs: { name: string; version?: number }[];
  skills: { id: string; label: string; description?: string; heroBound: boolean; boundBy: string[] }[];
  pendingSkills: string[];
  pendingGraph: { name: string; version?: number } | null;
  selectedHeroId: string | null;
  onSelectTemplate: (name: string) => void;
  onSelectHero: (heroId: string, handle: string) => void;
  onSelectGraph: (name: string, version?: number) => void;
  onToggleSkill: (id: string) => void;
  onClearGraph: () => void;
}

interface PickerState {
  kind: TriggerKind;
  query: string;
  start: number;
  caret: number;
  fromPlus: boolean;
}

/**
 * The composer's addressing surface: one picker behind `@`, `/`, and `+`.
 *
 * Owned by a hook rather than the component because three things move together —
 * the trigger token in the draft, the panel's own state, and the selection the
 * turn will carry — and keeping them in one place is what stops the draft and the
 * pills from disagreeing.
 */
export function useComposerPicker({
  input, setInput, data, canAct, onAttachFile, onAttachImage, onCompact,
}: {
  input: string;
  setInput: (value: string) => void;
  data: ComposerPickerData;
  /** False while a Run or a compaction locks the composer's actions. */
  canAct: boolean;
  onAttachFile: () => void;
  onAttachImage: () => void;
  onCompact: () => void;
}) {
  const t = useT();
  const [state, setState] = useState<PickerState | null>(null);
  const [plusFilter, setPlusFilter] = useState("");

  const sections: PickerSection[] = useMemo(() => {
    const selectable = data.skills.filter((skill) => !skill.heroBound);
    const bound = data.skills.filter((skill) => skill.heroBound);
    const groups: PickerSection[] = [
      {
        key: "actions", label: t("picker.actions"), pinned: true,
        items: [
          { key: "action:file", label: t("composer.attachFiles"), icon: <Paperclip size={14} aria-hidden="true" />, disabled: !canAct },
          { key: "action:image", label: t("composer.attachImage"), icon: <ImagePlus size={14} aria-hidden="true" />, disabled: !canAct },
          { key: "action:compact", label: t("composer.compactContext"), icon: <Minimize2 size={14} aria-hidden="true" />, disabled: !canAct },
        ],
      },
      {
        key: "heroes", label: t("picker.heroes"),
        items: data.heroes.map((hero) => ({
          key: `hero:${hero.id}`,
          label: `@${hero.handle}`,
          hint: hero.id === data.selectedHeroId ? t("picker.selected") : undefined,
          description: hero.description ?? hero.name,
          icon: <Bot size={14} aria-hidden="true" />,
        })),
      },
      {
        key: "graphs", label: t("picker.graphs"),
        items: data.graphs.map((graph) => ({
          key: `graph:${graph.name}`,
          label: graph.name,
          hint: graph.version ? `v${graph.version}` : undefined,
          icon: <Workflow size={14} aria-hidden="true" />,
        })),
      },
      {
        key: "skills", label: t("picker.skills"),
        items: selectable.map((skill) => ({
          key: `skill:${skill.id}`,
          label: `/${skill.label}`,
          hint: data.pendingSkills.includes(skill.id) ? t("picker.selected") : undefined,
          description: skill.description,
          icon: <Sparkles size={14} aria-hidden="true" />,
        })),
      },
      {
        key: "templates", label: t("picker.templates"),
        items: data.templates.map((template) => ({
          key: `template:${template.name}`,
          label: `/${template.name}`,
          hint: template.argumentHint || undefined,
          description: template.description,
          icon: <FileText size={14} aria-hidden="true" />,
        })),
      },
    ];
    if (bound.length > 0) {
      groups.push({
        key: "bound", label: t("picker.boundSkills"), readOnly: true,
        items: bound.map((skill) => ({
          key: `bound:${skill.id}`,
          label: `/${skill.label}`,
          hint: skill.boundBy.join(", "),
          icon: <Sparkles size={14} aria-hidden="true" />,
        })),
      });
    }
    return groups;
  }, [data, t, canAct]);

  /** Remove the token the picker owns; a `+` pick never had one. */
  const consumeToken = useCallback(() => {
    if (!state || state.fromPlus) return;
    setInput(input.slice(0, state.start) + input.slice(state.caret));
  }, [state, input, setInput]);

  const pick = useCallback((item: PickerItem, section: PickerSection) => {
    if (item.disabled) return;
    if (section.key === "actions") {
      if (item.key === "action:file") onAttachFile();
      if (item.key === "action:image") onAttachImage();
      if (item.key === "action:compact") onCompact();
      setState(null);
      return;
    }
    if (section.key === "heroes") {
      data.onSelectHero(item.key.slice("hero:".length), item.label.replace(/^@/, ""));
    } else if (section.key === "graphs") {
      const graph = data.graphs.find((entry) => `graph:${entry.name}` === item.key);
      data.onSelectGraph(item.key.slice("graph:".length), graph?.version);
    } else if (section.key === "skills") {
      data.onToggleSkill(item.key.slice("skill:".length));
      // Close after a pick: the token is gone, so keeping the panel open would
      // leave it filtering on a caret position that no longer exists. Picking a
      // second Skill is one `/` away.
      consumeToken();
      setState(null);
      return;
    } else if (section.key === "templates") {
      // A template replaces the draft, so the token goes first and the panel closes.
      if (state && !state.fromPlus) setInput(input.slice(0, state.start) + input.slice(state.caret));
      else if (state?.fromPlus) setInput("");
      data.onSelectTemplate(item.key.slice("template:".length));
      setState(null);
      return;
    }
    consumeToken();
    setState(null);
  }, [data, state, input, setInput, onAttachFile, onAttachImage, onCompact, consumeToken]);

  const openFromPlus = useCallback(() => {
    setPlusFilter("");
    setState({ kind: "all", query: "", start: 0, caret: 0, fromPlus: true });
  }, []);

  const onInputChange = useCallback((event: ChangeEvent<HTMLTextAreaElement>) => {
    const value = event.target.value;
    setInput(value);
    const caret = event.target.selectionStart ?? value.length;
    const trigger = detectComposerTrigger(value, caret);
    if (!trigger) {
      setState(null);
      return;
    }
    setState({ kind: trigger.kind, query: trigger.query, start: trigger.start, caret, fromPlus: false });
  }, [setInput]);

  return {
    open: state !== null,
    fromPlus: state?.fromPlus === true,
    filter: state?.fromPlus ? plusFilter : (state?.query ?? ""),
    sections,
    pick,
    openFromPlus,
    onInputChange,
    setPlusFilter,
    close: () => setState(null),
    toggle: () => (state ? setState(null) : openFromPlus()),
  };
}
