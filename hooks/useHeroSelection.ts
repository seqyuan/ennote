"use client";

import { useEffect, useLayoutEffect, useState } from "react";
import type { HeroSummary } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type GlobalHeroSummary = components["schemas"]["GlobalHeroSummary"];
type GlobalHeroDetail = components["schemas"]["GlobalHeroDetail"];
type FileRevision = components["schemas"]["ImmutableFileRevision"];

/**
 * Hero catalog + selection for the composer. Loads published global Heroes.
 * Selection does not survive a project switch (declarative, §4.1.3).
 */
export function useHeroSelection(selectedProject: string | null): {
  heroes: HeroSummary[];
  selectedHeroId: string | null;
  setSelectedHeroId: (id: string | null) => void;
} {
  const [selectedHeroId, setSelectedHeroId] = useState<string | null>(null);
  const [heroes, setHeroes] = useState<HeroSummary[]>([]);

  // Hero selection does not survive a project switch (declarative, §4.1.3).
  useLayoutEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSelectedHeroId(null);
  }, [selectedProject]);

  useEffect(() => {
    if (!selectedProject) return;
    let cancelled = false;
    void apiFetch<GlobalHeroSummary[]>("/v1/global-heroes")
      .then(async (catalog) => {
        // A malformed/empty payload must never take down the composer via
        // .filter on null; treat it as an empty catalog.
        const resolved = await Promise.all((catalog ?? []).filter((entry) => !entry.error).map(async (entry): Promise<HeroSummary | null> => {
          try {
            const [detail, revisions] = await Promise.all([
              apiFetch<GlobalHeroDetail>(`/v1/global-heroes/${encodeURIComponent(entry.id)}`),
              apiFetch<FileRevision[]>(`/v1/global-heroes/${encodeURIComponent(entry.id)}/versions`),
            ]);
            const latest = revisions.at(-1);
            if (!latest) return null;
            return {
              id: entry.id, handle: detail.document.handle, name: detail.document.name,
              description: detail.document.description, positioning: detail.document.positioning,
              icon: detail.document.icon, color: detail.document.color, scope: "global", status: "active",
              sourceKind: "managed", sourceLocator: detail.path,
              currentVersionId: `v${String(latest.version).padStart(6, "0")}`,
              currentVersion: latest.version, updatedAt: latest.publishedAt,
            };
          } catch {
            return null;
          }
        }));
        if (cancelled) return;
        const published = resolved.filter((role): role is HeroSummary => role !== null);
        setHeroes(published);
        setSelectedHeroId((current) => published.some((role) => role.id === current) ? current : null);
      })
      .catch(() => {
        if (!cancelled) setHeroes([]);
      });
    return () => { cancelled = true; };
  }, [selectedProject]);

  return { heroes, selectedHeroId, setSelectedHeroId };
}
