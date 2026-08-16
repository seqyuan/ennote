"use client";

import { useEffect, useLayoutEffect, useState } from "react";
import type { RoleSummary } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type GlobalRoleSummary = components["schemas"]["GlobalRoleSummary"];
type GlobalRoleDetail = components["schemas"]["GlobalRoleDetail"];
type FileRevision = { version: number; publishedAt: string };

/**
 * Role catalog + selection for the composer. Loads the published global Role
 * catalog (with a /v1/roles fallback for SQL-backed test adapters) and owns the
 * selected Role id. Selection does not survive a project switch (declarative,
 * §4.1.3).
 */
export function useRoleSelection(selectedProject: string | null): {
  roles: RoleSummary[];
  selectedRoleId: string | null;
  setSelectedRoleId: (id: string | null) => void;
} {
  const [selectedRoleId, setSelectedRoleId] = useState<string | null>(null);
  const [roles, setRoles] = useState<RoleSummary[]>([]);

  // Role selection does not survive a project switch (declarative, §4.1.3).
  useLayoutEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSelectedRoleId(null);
  }, [selectedProject]);

  // Role catalog: global roles with a /v1/roles fallback.
  useEffect(() => {
    if (!selectedProject) return;
    let cancelled = false;
    void apiFetch<GlobalRoleSummary[]>("/v1/global-roles")
      .then(async (catalog) => {
        const resolved = await Promise.all(catalog.filter((entry) => !entry.error).map(async (entry): Promise<RoleSummary | null> => {
          try {
            const [detail, revisions] = await Promise.all([
              apiFetch<GlobalRoleDetail>(`/v1/global-roles/${encodeURIComponent(entry.id)}`),
              apiFetch<FileRevision[]>(`/v1/global-roles/${encodeURIComponent(entry.id)}/versions`),
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
        const published = resolved.filter((role): role is RoleSummary => role !== null);
        setRoles(published);
        setSelectedRoleId((current) => published.some((role) => role.id === current) ? current : null);
      })
      .catch(async () => {
        // SQL-backed API test adapters expose the managed Role catalog.
        try {
          const params = new URLSearchParams({ projectId: selectedProject, status: "active", limit: "100" });
          const page = await apiFetch<{ items: RoleSummary[] }>(`/v1/roles?${params}`);
          if (cancelled) return;
          const published = page.items.filter((role) => Boolean(role.currentVersionId));
          setRoles(published);
          setSelectedRoleId((current) => published.some((role) => role.id === current) ? current : null);
        } catch {
          if (!cancelled) setRoles([]);
        }
      });
    return () => { cancelled = true; };
  }, [selectedProject]);

  return { roles, selectedRoleId, setSelectedRoleId };
}
