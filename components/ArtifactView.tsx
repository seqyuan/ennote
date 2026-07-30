"use client";

import { Download, FileCode2, FileText, Image as ImageIcon, Table2 } from "lucide-react";
import { useEffect, useState } from "react";
import { ArtifactTablePreview } from "@/components/ArtifactTablePreview";
import {
  artifactDownloadURL, artifactPreviewURL, formatArtifactSize, type ArtifactReference, type TextPreview,
} from "@/lib/artifacts";
import { apiFetch } from "@/lib/worker-api.client";

export function ArtifactView({ sessionId, artifact }: { sessionId: string; artifact: ArtifactReference }) {
  const previewURL = artifactPreviewURL(sessionId, artifact.artifactId);
  const downloadURL = artifactDownloadURL(sessionId, artifact.artifactId);
  return <section className="artifact-result" aria-label={`Artifact ${artifact.name}`}
    data-artifact-kind={artifact.kind} data-artifact-id={artifact.artifactId}>
    <header className="artifact-result-header">
      <span className="artifact-result-icon">{artifactIcon(artifact.kind)}</span>
      <span className="artifact-result-name" title={artifact.name}>{artifact.name}</span>
      <span className="artifact-result-meta">{artifact.mimeType} · {formatArtifactSize(artifact.sizeBytes)}</span>
      <a className="artifact-download" href={downloadURL} download={artifact.name}
        aria-label={`Download ${artifact.name}`} title={`Download ${artifact.name}`}>
        <Download size={15} aria-hidden="true" />
      </a>
    </header>
    {artifact.kind === "image" && <ArtifactImage artifact={artifact} previewURL={previewURL} />}
    {artifact.kind === "table" && <ArtifactTablePreview sessionId={sessionId} artifact={artifact} />}
    {artifact.kind === "static_html" && <iframe className="artifact-html-preview" src={previewURL}
      title={`${artifact.name} preview`} sandbox="" referrerPolicy="no-referrer" loading="lazy" />}
    {artifact.kind === "text" && <ArtifactTextPreview sessionId={sessionId} artifact={artifact} />}
  </section>;
}

function ArtifactImage({ artifact, previewURL }: { artifact: ArtifactReference; previewURL: string }) {
  const [failed, setFailed] = useState(false);
  if (failed) return <div className="artifact-preview-state is-error" role="alert">Preview unavailable</div>;
  return <figure className="artifact-image-preview">
    {/* eslint-disable-next-line @next/next/no-img-element */}
    <img src={previewURL} alt={artifact.name} width={artifact.width} height={artifact.height}
      loading="lazy" onError={() => setFailed(true)} />
    {(artifact.width || artifact.height) && <figcaption>{artifact.width} x {artifact.height}</figcaption>}
  </figure>;
}

function ArtifactTextPreview({ sessionId, artifact }: { sessionId: string; artifact: ArtifactReference }) {
  const [preview, setPreview] = useState<TextPreview | null>(null);
  const [error, setError] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void apiFetch<TextPreview>(artifactPreviewURL(sessionId, artifact.artifactId), { signal: controller.signal })
      .then(setPreview)
      .catch(reason => {
        if ((reason as Error).name !== "AbortError") setError(true);
      });
    return () => controller.abort();
  }, [artifact.artifactId, sessionId]);
  if (error) return <div className="artifact-preview-state is-error" role="alert">Preview unavailable</div>;
  if (!preview) return <div className="artifact-preview-state" role="status" aria-live="polite">Loading preview...</div>;
  return <div className="artifact-text-preview">
    <pre>{preview.text}</pre>
    {preview.truncated && <span>Preview truncated</span>}
  </div>;
}

function artifactIcon(kind: ArtifactReference["kind"]) {
  const props = { size: 15, "aria-hidden": true } as const;
  if (kind === "image") return <ImageIcon {...props} />;
  if (kind === "table") return <Table2 {...props} />;
  if (kind === "static_html") return <FileCode2 {...props} />;
  return <FileText {...props} />;
}
