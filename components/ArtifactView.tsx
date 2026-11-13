"use client";

import { Download, FileCode2, FileText, Image as ImageIcon, PanelRight, Pin, PinOff, Sparkles, Table2 } from "lucide-react";
import { useEffect, useState } from "react";
import { ArtifactTablePreview } from "@/components/ArtifactTablePreview";
import { ArtifactWidget } from "@/components/ArtifactWidget";
import { useT } from "@/components/LocaleProvider";
import {
  artifactDownloadURL, artifactMetadataURL, artifactPreviewURL, formatArtifactSize, isWidgetArtifact,
  type ArtifactRecord, type ArtifactReference, type TextPreview,
} from "@/lib/artifacts";
import { requestOpenArtifact } from "@/lib/canvas-intent";
import { apiFetch } from "@/lib/worker-api.client";

/**
 * One artifact row: header, preview, download.
 *
 * `inCanvas` is set by the inspector lane, which draws this same section for the
 * artifact it already contains. The "open in the canvas" action is suppressed
 * there: a control that re-opens the surface it sits in does nothing, and a
 * button that does nothing is worse than no button.
 */
export function ArtifactView({ sessionId, artifact, inCanvas = false }: {
  sessionId: string;
  artifact: ArtifactReference;
  inCanvas?: boolean;
}) {
  const t = useT();
  const previewURL = artifactPreviewURL(sessionId, artifact.artifactId);
  const downloadURL = artifactDownloadURL(sessionId, artifact.artifactId);
  const openLabel = t("conversation.artifact.openInCanvas", { name: artifact.name });

  // Pin state is Worker state, not panel state: the canvas channel re-fetches on
  // its own mount, so there is no shared store to keep in step. The reference a
  // message carries has no `pinnedAt`, so the full record is read once here and
  // the toggle writes straight to the Worker and adopts the refreshed record's
  // answer. `null` means "not known yet", which disables the toggle rather than
  // let a click before the fetch resolve double-pin.
  const [pinned, setPinned] = useState<boolean | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    void apiFetch<ArtifactRecord>(artifactMetadataURL(sessionId, artifact.artifactId), { signal: controller.signal })
      .then((record) => {
        if (!controller.signal.aborted) setPinned(record.pinnedAt != null);
      })
      .catch(() => { /* leave unknown: the toggle stays disabled until the Worker answers */ });
    return () => controller.abort();
  }, [artifact.artifactId, sessionId]);

  const togglePin = async () => {
    // Both pin and unpin answer 200 with the refreshed artifact; adopt its
    // `pinnedAt` rather than flip optimistically, so a rejected write cannot
    // leave the button asserting a state the Worker does not have.
    const path = `${artifactMetadataURL(sessionId, artifact.artifactId)}/pin`;
    const method = pinned ? "DELETE" : "POST";
    try {
      const refreshed = await apiFetch<ArtifactRecord>(path, { method });
      setPinned(refreshed.pinnedAt != null);
    } catch { /* leave the state the Worker last reported */ }
  };
  const pinLabel = pinned
    ? t("conversation.artifact.unpin", { name: artifact.name })
    : t("conversation.artifact.pin", { name: artifact.name });

  return <section className="artifact-result" aria-label={`Artifact ${artifact.name}`}
    data-artifact-kind={artifact.kind} data-artifact-id={artifact.artifactId}>
    <header className="artifact-result-header">
      <span className="artifact-result-icon">{artifactIcon(artifact.kind)}</span>
      <span className="artifact-result-name" title={artifact.name}>{artifact.name}</span>
      <span className="artifact-result-meta">{artifact.mimeType} · {formatArtifactSize(artifact.sizeBytes)}</span>
      {/* Every kind opens, not just widgets: the lane draws an artifact with the
          same surface this section uses, so there is no kind left over with
          nowhere to go. The click only states the intent — the panel decides
          whether it can honour it. */}
      {!inCanvas && <button type="button" className="artifact-canvas-open"
        onClick={() => requestOpenArtifact({ sessionId, artifactId: artifact.artifactId, name: artifact.name })}
        aria-label={openLabel} title={openLabel}>
        <PanelRight size={15} aria-hidden="true" />
      </button>}
      {!inCanvas && <button type="button" className="artifact-pin-toggle"
        onClick={() => void togglePin()} disabled={pinned === null}
        aria-pressed={pinned === true} aria-label={pinLabel} title={pinLabel}>
        {pinned ? <Pin size={15} aria-hidden="true" /> : <PinOff size={15} aria-hidden="true" />}
      </button>}
      <a className="artifact-download" href={downloadURL} download={artifact.name}
        aria-label={`Download ${artifact.name}`} title={`Download ${artifact.name}`}>
        <Download size={15} aria-hidden="true" />
      </a>
    </header>
    {artifact.kind === "image" && <ArtifactImage artifact={artifact} previewURL={previewURL} />}
    {artifact.kind === "table" && <ArtifactTablePreview sessionId={sessionId} artifact={artifact} />}
    {artifact.kind === "static_html" && <iframe className="artifact-html-preview" src={previewURL}
      title={`${artifact.name} preview`} sandbox="" referrerPolicy="no-referrer" loading="lazy" />}
    {isWidgetArtifact(artifact) && <ArtifactWidget sessionId={sessionId} artifact={artifact} />}
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
  if (kind === "widget") return <Sparkles {...props} />;
  return <FileText {...props} />;
}
