import type { components } from "@/lib/worker-api.gen";

export type ArtifactReference = components["schemas"]["ArtifactReference"];
export type ArtifactRecord = components["schemas"]["Artifact"];
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

/**
 * Whether this artifact is a widget — the only kind the client renders with scripts
 * enabled. Kept next to the other artifact predicates so a caller cannot open-code a
 * string comparison and drift.
 */
export function isWidgetArtifact(artifact: { kind: string }): boolean {
  return artifact.kind === "widget";
}

/**
 * Read the `getArtifact` answer as the reference a message carries.
 *
 * The two shapes differ by more than their key for the id: a stored row also
 * knows its Project, Run and retention class, which the renderer neither needs
 * nor may be told — the pane draws the artifact, not its provenance. The
 * attachment kind is folded into `image` because only a message can publish a
 * reference, and an attached image is drawn by the same image branch.
 */
export function artifactReferenceOf(record: ArtifactRecord): ArtifactReference {
  return {
    artifactId: record.id,
    name: record.name,
    kind: record.kind === "image_attachment" ? "image" : record.kind,
    mimeType: record.mimeType,
    sizeBytes: record.sizeBytes,
    sha256: record.sha256,
    width: record.width,
    height: record.height,
  };
}

export function formatArtifactSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(bytes < 10 * 1024 * 1024 ? 1 : 0)} MB`;
}
