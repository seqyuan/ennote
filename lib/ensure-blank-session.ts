import type { Session } from "@/components/settings/types";
import { isBlankSession } from "@/lib/session-blank";
import { apiFetch } from "@/lib/worker-api.client";

export const SELECTED_PROJECT_KEY = "ennote-selected-project";
export const SELECTED_SESSION_KEY = "ennote-selected-session";

export function readStoredId(key: string): string | null {
  try {
    const value = localStorage.getItem(key);
    return value && value.length > 0 ? value : null;
  } catch {
    return null;
  }
}

export function writeStoredId(key: string, id: string | null): void {
  try {
    if (id) localStorage.setItem(key, id);
    else localStorage.removeItem(key);
  } catch {
    /* storage unavailable */
  }
}

/** dsh recentWorkspace: newest `updatedAt`, then `createdAt`. */
export function recentProjectId(
  projects: readonly { id: string; updatedAt: string; createdAt: string }[],
): string | undefined {
  if (projects.length === 0) return undefined;
  let best = projects[0];
  let bestTime = Date.parse(best.updatedAt) || Date.parse(best.createdAt) || 0;
  for (const project of projects.slice(1)) {
    const time = Date.parse(project.updatedAt) || Date.parse(project.createdAt) || 0;
    if (time > bestTime) {
      best = project;
      bestTime = time;
    }
  }
  return best.id;
}

/**
 * dsh connectWorkspace: reuse the project's unused blank session, or create one.
 */
export async function reuseOrCreateBlankSession(projectId: string, known: Session[]): Promise<Session> {
  const pool = known.some((session) => session.projectId === projectId)
    ? known.filter((session) => session.projectId === projectId)
    : await apiFetch<Session[]>(`/v1/projects/${encodeURIComponent(projectId)}/sessions?status=active`);
  const existing = pool.find(isBlankSession);
  if (existing) return existing;
  const created = await apiFetch<Session>(`/v1/projects/${encodeURIComponent(projectId)}/sessions`, {
    method: "POST",
    body: JSON.stringify({ title: "New Chat" }),
  });
  if (!created?.id) throw new Error("Failed to create session");
  return created;
}
