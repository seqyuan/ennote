"use client";

import { useCallback, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import { confirmAction } from "@/components/ui/confirm";
import { useLocale } from "@/components/LocaleProvider";
import { translate } from "@/lib/messages";
import { useSettingsStatus } from "@/components/settings/SettingsStatus";
import type { AnnotatedSkill, SkillInstallInfo, SkillUpdateResult } from "@/components/settings/types";

export function useSkillRowActions(
  projectId: string | null,
  setSkills: Dispatch<SetStateAction<AnnotatedSkill[]>>,
  reload: () => Promise<void>,
  setError: (value: string | null) => void,
): {
  updateKey: (install: { package: string; scope: string }) => string;
  updates: Record<string, SkillUpdateResult>;
  checking: string | null;
  toggling: string | null;
  removing: string | null;
  check: (install: SkillInstallInfo) => Promise<void>;
  update: (install: SkillInstallInfo) => Promise<void>;
  toggle: (skill: AnnotatedSkill) => Promise<void>;
  remove: (skill: AnnotatedSkill) => Promise<void>;
} {
  const [updates, setUpdates] = useState<Record<string, SkillUpdateResult>>({});
  const [checking, setChecking] = useState<string | null>(null);
  const [toggling, setToggling] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const { markSaving, markSaved } = useSettingsStatus();
  const { locale } = useLocale();
  // The hook builds user-visible fallbacks and confirm copy, so it translates
  // them itself; a board translated only at the JSX layer would answer in
  // English the moment an operation failed.
  const t = useCallback(
    (key: string, params?: Record<string, string | number>) => translate(locale, key, params),
    [locale],
  );

  const updateKey = useCallback((install: { package: string; scope: string }) => `${install.scope}\u0000${install.package}`, []);

  const check = useCallback(async (install: SkillInstallInfo) => {
    const key = updateKey(install);
    setChecking(key);
    try {
      const data = await apiFetch<{ updates: SkillUpdateResult[] }>("/v1/skills/check", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          package: install.package, scope: install.scope,
          ...(install.scope === "project" && projectId ? { projectId } : {}),
        }),
      });
      setUpdates((current) => {
        const next = { ...current };
        for (const item of data.updates ?? []) next[updateKey(item)] = item;
        return next;
      });
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("settings.skills.error.updateCheck"));
    } finally {
      setChecking(null);
    }
  }, [projectId, setError, updateKey]);

  const update = useCallback(async (install: SkillInstallInfo) => {
    const key = updateKey(install);
    setChecking(key);
    markSaving();
    try {
      await apiFetch<{ success: boolean; output?: string }>("/v1/skills/update", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          package: install.package, scope: install.scope,
          ...(install.scope === "project" && projectId ? { projectId } : {}),
        }),
      });
      setError(null);
      setUpdates((current) => {
        const next = { ...current };
        delete next[key];
        return next;
      });
      await reload();
      markSaved();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("settings.skills.error.update"));
    } finally {
      setChecking(null);
    }
  }, [projectId, setError, updateKey, reload, markSaving, markSaved]);

  const toggle = useCallback(async (skill: AnnotatedSkill) => {
    setToggling(skill.relPath);
    markSaving();
    try {
      await apiFetch(`/v1/skills/disabled/${skill.relPath}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ disabled: !skill.disableModelInvocation }),
      });
      setSkills((current) => current.map((item) =>
        item.relPath === skill.relPath ? { ...item, disableModelInvocation: !skill.disableModelInvocation } : item));
      setError(null);
      markSaved();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("settings.skills.error.toggle"));
    } finally {
      setToggling(null);
    }
  }, [setSkills, setError, markSaving, markSaved]);

  const remove = useCallback(async (skill: AnnotatedSkill) => {
    if (!(await confirmAction({
      title: t("settings.skills.confirm.removeTitle", { name: skill.name }),
      description: t("settings.skills.confirm.removeDescription"),
      confirmLabel: t("settings.skills.confirm.removeLabel"),
      tone: "danger",
    }))) return;
    setRemoving(skill.relPath);
    markSaving();
    try {
      await apiFetch(`/v1/skills/remove/${skill.relPath}`, { method: "POST" });
      setSkills((current) => current.filter((item) => item.relPath !== skill.relPath));
      setError(null);
      markSaved();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("settings.skills.error.remove"));
    } finally {
      setRemoving(null);
    }
  }, [setSkills, setError, markSaving, markSaved]);

  return { updateKey, updates, checking, toggling, removing, check, update, toggle, remove };
}
