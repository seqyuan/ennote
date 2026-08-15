"use client";

import { useCallback, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { apiFetch } from "@/lib/worker-api.client";
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
      setError(reason instanceof Error ? reason.message : "Update check failed");
    } finally {
      setChecking(null);
    }
  }, [projectId, setError, updateKey]);

  const update = useCallback(async (install: SkillInstallInfo) => {
    const key = updateKey(install);
    setChecking(key);
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
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Update failed");
    } finally {
      setChecking(null);
    }
  }, [projectId, setError, updateKey, reload]);

  const toggle = useCallback(async (skill: AnnotatedSkill) => {
    setToggling(skill.relPath);
    try {
      await apiFetch(`/v1/skills/disabled/${skill.relPath}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ disabled: !skill.disableModelInvocation }),
      });
      setSkills((current) => current.map((item) =>
        item.relPath === skill.relPath ? { ...item, disableModelInvocation: !skill.disableModelInvocation } : item));
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Toggle failed");
    } finally {
      setToggling(null);
    }
  }, [setSkills, setError]);

  const remove = useCallback(async (skill: AnnotatedSkill) => {
    if (!window.confirm(`Remove the skill "${skill.name}"?`)) return;
    setRemoving(skill.relPath);
    try {
      await apiFetch(`/v1/skills/remove/${skill.relPath}`, { method: "POST" });
      setSkills((current) => current.filter((item) => item.relPath !== skill.relPath));
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Remove failed");
    } finally {
      setRemoving(null);
    }
  }, [setSkills, setError]);

  return { updateKey, updates, checking, toggling, removing, check, update, toggle, remove };
}
