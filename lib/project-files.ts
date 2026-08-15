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
