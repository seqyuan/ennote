"use client";

import { useCallback, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import { confirmAction } from "@/components/ui/confirm";
import { useLocale } from "@/components/LocaleProvider";
import { translate } from "@/lib/messages";
import { useSettingsStatus } from "@/components/settings/SettingsStatus";
import type { SkillRoot } from "@/components/settings/types";

export function useSkillRoots(
  setRoots: Dispatch<SetStateAction<SkillRoot[]>>,
  reload: () => Promise<void>,
  setError: (value: string | null) => void,
): {
  showRootForm: boolean;
  setShowRootForm: (open: boolean) => void;
  rootName: string;
  setRootName: (value: string) => void;
  rootPreset: string;
  setRootPreset: (value: string) => void;
  rootPath: string;
  setRootPath: (value: string) => void;
  rootBusy: string | null;
  addRoot: () => Promise<void>;
  toggleRoot: (root: SkillRoot) => Promise<void>;
  removeRoot: (root: SkillRoot) => Promise<void>;
} {
  const [showRootForm, setShowRootForm] = useState(false);
  const [rootName, setRootName] = useState("");
  const [rootPreset, setRootPreset] = useState("pi");
  const [rootPath, setRootPath] = useState("");
  const [rootBusy, setRootBusy] = useState<string | null>(null);
  const { markSaving, markSaved } = useSettingsStatus();
  const { locale } = useLocale();
  // The hook builds user-visible fallbacks and confirm copy, so it translates
  // them itself; a board translated only at the JSX layer would answer in
  // English the moment an operation failed.
  const t = useCallback(
    (key: string, params?: Record<string, string | number>) => translate(locale, key, params),
    [locale],
  );

  const addRoot = useCallback(async () => {
    setRootBusy("add");
    markSaving();
    try {
      await apiFetch<SkillRoot>("/v1/skills/roots", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: rootName.trim() || rootPreset,
          agentKind: rootPath.trim() ? "generic" : rootPreset,
          path: rootPath.trim() || undefined,
        }),
      });
      setRootName("");
      setRootPath("");
      setShowRootForm(false);
      setError(null);
      await reload();
      markSaved();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("settings.skills.error.addRoot"));
    } finally {
      setRootBusy(null);
    }
  }, [rootName, rootPreset, rootPath, setError, reload, markSaving, markSaved]);

  const toggleRoot = useCallback(async (root: SkillRoot) => {
    setRootBusy(root.id);
    markSaving();
    try {
      const updated = await apiFetch<SkillRoot>(`/v1/skills/roots/${root.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: !root.enabled }),
      });
      setRoots((current) => current.map((item) => item.id === root.id ? updated : item));
      setError(null);
      await reload();
      markSaved();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("settings.skills.error.toggle"));
    } finally {
      setRootBusy(null);
    }
  }, [setRoots, setError, reload, markSaving, markSaved]);

  const removeRoot = useCallback(async (root: SkillRoot) => {
    if (!(await confirmAction({
      title: t("settings.skills.confirm.stopTitle", { path: root.path }),
      description: t("settings.skills.confirm.stopDescription"),
      confirmLabel: t("settings.skills.confirm.stopLabel"),
      tone: "danger",
    }))) return;
    setRootBusy(root.id);
    markSaving();
    try {
      await apiFetch(`/v1/skills/roots/${root.id}`, { method: "DELETE" });
      setRoots((current) => current.filter((item) => item.id !== root.id));
      setError(null);
      await reload();
      markSaved();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("settings.skills.error.removeRoot"));
    } finally {
      setRootBusy(null);
    }
  }, [setRoots, setError, reload, markSaving, markSaved]);

  return {
    showRootForm, setShowRootForm,
    rootName, setRootName,
    rootPreset, setRootPreset,
    rootPath, setRootPath,
    rootBusy,
    addRoot, toggleRoot, removeRoot,
  };
}
