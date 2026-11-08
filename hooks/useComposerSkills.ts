"use client";

import { useCallback, useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";

export interface ComposerSkill {
  id: string;
  label: string;
  description?: string;
  /** Loaded by some Hero already, so it is offered read-only rather than selectable. */
  heroBound: boolean;
  boundBy: string[];
}

type SkillRow = {
  skillId?: string;
  name?: string;
  description?: string;
  heroBound?: boolean;
  boundBy?: string[];
};

/**
 * The composer's Skill catalogue.
 *
 * Project-scope Skills are deliberately not listed: a selection travels with a
 * turn and the Worker resolves ids from the global catalogue, so offering a
 * project Skill here would promise something the submit rejects. `heroBound`
 * tells the picker which rows already arrive with a Hero, so the panel can show
 * them in a read-only group instead of hiding them without explanation.
 */
export function useComposerSkills(projectId: string | null): {
  skills: ComposerSkill[];
  refresh: () => Promise<void>;
} {
  const [skills, setSkills] = useState<ComposerSkill[]>([]);

  const refresh = useCallback(async () => {
    try {
      const query = projectId ? `?${new URLSearchParams({ projectID: projectId })}` : "";
      const result = await apiFetch<{ skills?: SkillRow[] }>(`/v1/skills${query}`);
      setSkills((result?.skills ?? [])
        .filter((row) => Boolean(row.skillId))
        .map((row) => ({
          id: row.skillId ?? "",
          label: row.name || row.skillId || "",
          description: row.description,
          heroBound: row.heroBound === true,
          boundBy: row.boundBy ?? [],
        })));
    } catch {
      // The picker is a convenience; a failed catalogue leaves it empty and the
      // composer still sends plain turns.
    }
  }, [projectId]);

  // Fetch on mount and whenever the project changes. The refresh is async, so
  // the setState happens in its continuation rather than in the effect body.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      if (cancelled) return;
      await refresh();
    })();
    return () => { cancelled = true; };
  }, [refresh]);

  return { skills, refresh };
}
