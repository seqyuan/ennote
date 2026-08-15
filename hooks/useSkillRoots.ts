"use client";

import { useCallback, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { apiFetch } from "@/lib/worker-api.client";
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

  const addRoot = useCallback(async () => {
    setRootBusy("add");
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
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Failed to add skill root");
    } finally {
      setRootBusy(null);
    }
  }, [rootName, rootPreset, rootPath, setError, reload]);

  const toggleRoot = useCallback(async (root: SkillRoot) => {
    setRootBusy(root.id);
    try {
      const updated = await apiFetch<SkillRoot>(`/v1/skills/roots/${root.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: !root.enabled }),
      });
      setRoots((current) => current.map((item) => item.id === root.id ? updated : item));
      setError(null);
      await reload();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Toggle failed");
    } finally {
      setRootBusy(null);
    }
  }, [setRoots, setError, reload]);

  const removeRoot = useCallback(async (root: SkillRoot) => {
    if (!window.confirm(`Stop reading skills from "${root.path}"?`)) return;
    setRootBusy(root.id);
    try {
      await apiFetch(`/v1/skills/roots/${root.id}`, { method: "DELETE" });
      setRoots((current) => current.filter((item) => item.id !== root.id));
      setError(null);
      await reload();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Remove failed");
    } finally {
      setRootBusy(null);
    }
  }, [setRoots, setError, reload]);

  return {
    showRootForm, setShowRootForm,
    rootName, setRootName,
    rootPreset, setRootPreset,
    rootPath, setRootPath,
    rootBusy,
    addRoot, toggleRoot, removeRoot,
  };
}
