"use client";

import { useCallback, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import { useSettingsStatus } from "@/components/settings/SettingsStatus";
import type { SkillCreateResult } from "@/components/settings/types";

// allowedToolsText is the raw comma-separated field the user types; the Worker
// receives a trimmed, de-duplicated array.
function parseAllowedTools(text: string): string[] {
  const seen = new Set<string>();
  const tools: string[] = [];
  for (const part of text.split(",")) {
    const tool = part.trim();
    if (!tool || seen.has(tool)) continue;
    seen.add(tool);
    tools.push(tool);
  }
  return tools;
}

export function useSkillCreate(
  reload: () => Promise<void>,
  setError: (value: string | null) => void,
): {
  showForm: boolean;
  setShowForm: (open: boolean) => void;
  id: string;
  setId: (value: string) => void;
  name: string;
  setName: (value: string) => void;
  description: string;
  setDescription: (value: string) => void;
  prompt: string;
  setPrompt: (value: string) => void;
  allowedToolsText: string;
  setAllowedToolsText: (value: string) => void;
  busy: boolean;
  canSubmit: boolean;
  create: () => Promise<boolean>;
} {
  const [showForm, setShowForm] = useState(false);
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [prompt, setPrompt] = useState("");
  const [allowedToolsText, setAllowedToolsText] = useState("");
  const [busy, setBusy] = useState(false);
  const { markSaving, markSaved } = useSettingsStatus();

  const trimmedID = id.trim();
  const trimmedPrompt = prompt.trim();
  const canSubmit = !busy && trimmedID.length > 0 && trimmedPrompt.length > 0;

  const create = useCallback(async (): Promise<boolean> => {
    const skillID = id.trim();
    const skillPrompt = prompt.trim();
    // The submit button is already disabled in these two cases; keep the guard
    // so an Enter keypress in a field cannot post an incomplete Skill.
    if (!skillID || !skillPrompt) {
      setError("Skill ID and prompt are required");
      return false;
    }
    setBusy(true);
    markSaving();
    try {
      await apiFetch<SkillCreateResult>("/v1/skills", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: skillID,
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          prompt: skillPrompt,
          allowedTools: parseAllowedTools(allowedToolsText),
        }),
      });
      setId("");
      setName("");
      setDescription("");
      setPrompt("");
      setAllowedToolsText("");
      setShowForm(false);
      setError(null);
      // The Worker catalog is already live; reloading makes the new Skill
      // appear in this list without a restart or manual refresh.
      await reload();
      markSaved();
      return true;
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Failed to create skill");
      return false;
    } finally {
      setBusy(false);
    }
  }, [id, name, description, prompt, allowedToolsText, setError, reload, markSaving, markSaved]);

  return {
    showForm, setShowForm,
    id, setId,
    name, setName,
    description, setDescription,
    prompt, setPrompt,
    allowedToolsText, setAllowedToolsText,
    busy, canSubmit,
    create,
  };
}
