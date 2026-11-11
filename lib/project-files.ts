import type { components } from "@/lib/worker-api.gen";
import { apiFetch, apiResponse } from "@/lib/worker-api.client";

export type WorkspaceFileEntry = components["schemas"]["WorkspaceFileEntry"];
export type ProjectWorkspace = components["schemas"]["ProjectWorkspace"];

export function projectFilesPath(projectId: string, virtualPath: string): string {
  const params = new URLSearchParams({ path: virtualPath });
  return `/v1/projects/${encodeURIComponent(projectId)}/files?${params.toString()}`;
}

export function projectFileContentPath(projectId: string, virtualPath: string): string {
  const params = new URLSearchParams({ path: virtualPath });
  return `/v1/projects/${encodeURIComponent(projectId)}/files/content?${params.toString()}`;
}

export async function listProjectFiles(projectId: string, virtualPath = "/workspace", signal?: AbortSignal) {
  return apiFetch<WorkspaceFileEntry[]>(projectFilesPath(projectId, virtualPath), { signal });
}

export async function fetchProjectFile(projectId: string, virtualPath: string, signal?: AbortSignal): Promise<Response> {
  const response = await apiResponse(projectFileContentPath(projectId, virtualPath), { signal });
  if (!response.ok) {
    const body = await response.json().catch(() => ({})) as { error?: { message?: string } };
    throw new Error(body.error?.message ?? `HTTP ${response.status}`);
  }
  return response;
}

/** Split a virtual path into its parent and its last segment. */
export function splitWorkspacePath(virtualPath: string): { parent: string; name: string } {
  const trimmed = virtualPath.replace(/\/+$/, "");
  const index = trimmed.lastIndexOf("/");
  if (index <= 0) return { parent: "/workspace", name: trimmed };
  return { parent: trimmed.slice(0, index), name: trimmed.slice(index + 1) };
}

/**
 * Create one file or directory.
 *
 * The Worker refuses an existing target rather than overwriting it, so a name
 * clash surfaces as an error the reader can act on instead of lost contents.
 */
export async function createProjectEntry(
  projectId: string,
  input: { path: string; kind: "file" | "directory"; content?: string },
): Promise<WorkspaceFileEntry> {
  return apiFetch<WorkspaceFileEntry>(`/v1/projects/${encodeURIComponent(projectId)}/files/create`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

/** Rename or move one entry; both ends must stay inside /workspace. */
export async function moveProjectEntry(
  projectId: string,
  input: { from: string; to: string },
): Promise<WorkspaceFileEntry> {
  return apiFetch<WorkspaceFileEntry>(`/v1/projects/${encodeURIComponent(projectId)}/files/move`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

/** Delete one entry. A directory needs `recursive` once it has contents. */
export async function deleteProjectEntry(
  projectId: string,
  virtualPath: string,
  options: { recursive?: boolean } = {},
): Promise<void> {
  const params = new URLSearchParams({ path: virtualPath });
  if (options.recursive) params.set("recursive", "true");
  await apiFetch<void>(`/v1/projects/${encodeURIComponent(projectId)}/files?${params.toString()}`, {
    method: "DELETE",
  });
}
