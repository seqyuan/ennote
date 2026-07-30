import type { components } from "@/lib/worker-api.gen";

export type ArtifactReference = components["schemas"]["ArtifactReference"];
export type TablePreview = components["schemas"]["TablePreview"];
export type TextPreview = components["schemas"]["TextPreview"];

const encode = encodeURIComponent;

export function artifactMetadataURL(sessionId: string, artifactId: string): string {
  return `/api/worker/v1/sessions/${encode(sessionId)}/artifacts/${encode(artifactId)}`;
}

export function artifactPreviewURL(sessionId: string, artifactId: string, sheet?: string): string {
  const base = `${artifactMetadataURL(sessionId, artifactId)}/preview`;
  return sheet ? `${base}?${new URLSearchParams({ sheet }).toString()}` : base;
}

export function artifactDownloadURL(sessionId: string, artifactId: string): string {
  return `${artifactMetadataURL(sessionId, artifactId)}/download`;
}

export function formatArtifactSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(bytes < 10 * 1024 * 1024 ? 1 : 0)} MB`;
}
