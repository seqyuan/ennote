"use client";

import { useEffect, useState } from "react";

import { ArtifactView } from "@/components/ArtifactView";
import { useT } from "@/components/LocaleProvider";
import { artifactMetadataURL, artifactReferenceOf, type ArtifactReference, type ArtifactRecord } from "@/lib/artifacts";
import { apiFetch } from "@/lib/worker-api.client";

/**
 * One artifact, drawn in the inspector lane.
 *
 * The lane stores a row, not bytes: a tab holds (session, artifact) ids and
 * fetches the reference again on every mount, so a reload, a Worker restart or
 * a Project switch cannot show an artifact from a previous generation as if it
 * were current. That also means this surface has three states of its own, and
 * all of them stay inside the pane: a deleted artifact draws an error in the
 * lane and leaves the strip, the other pane and the conversation alone.
 */
export function ArtifactCanvasTab({ sessionId, artifactId, name }: {
  sessionId: string;
  artifactId: string;
  name: string;
}) {
  const t = useT();
  const [fetched, setFetched] = useState<Fetched | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    void apiFetch<ArtifactRecord>(artifactMetadataURL(sessionId, artifactId), { signal: controller.signal })
      .then((record) => {
        if (!controller.signal.aborted) {
          setFetched({ sessionId, artifactId, artifact: artifactReferenceOf(record) });
        }
      })
      .catch((reason: Error) => {
        // An abort means the row was closed or moved on, which is not a failure
        // anyone is waiting to read.
        if (!controller.signal.aborted) setFetched({ sessionId, artifactId, message: reason.message });
      });
    return () => controller.abort();
  }, [artifactId, sessionId, attempt]);

  // An answer only counts for the artifact it was fetched for. The parent keys this
  // component by tab id today, so a remount also covers it; the check stays because
  // it is what makes that an optimisation rather than the guarantee.
  const current = fetched?.sessionId === sessionId && fetched.artifactId === artifactId ? fetched : null;

  if (current === null) {
    return <div className="artifact-canvas-state" role="status">
      {t("rightPanel.artifact.loading")}
    </div>;
  }
  if ("message" in current) {
    return <div className="artifact-canvas-state is-error" role="alert">
      {/* The sentence states what was observed; the cause goes underneath as the
          Worker said it. "It may have been deleted" would be a guess, and a wrong
          one for a network failure or a Worker restart. */}
      <span>{t("rightPanel.artifact.loadFailed", { name })}</span>
      <span className="artifact-canvas-detail" title={current.message}>{current.message}</span>
      {/* A transient failure otherwise strands this pane: the row's id cannot change,
          so the effect never runs again on its own. Same affordance as the frame's. */}
      <button type="button" className="artifact-widget-action" onClick={() => {
        setFetched(null);
        setAttempt(value => value + 1);
      }}>
        {t("conversation.artifact.widget.retry")}
      </button>
    </div>;
  }
  // The widget's sandbox, envelope and height protocol are `ArtifactView`'s
  // business; a second frame implementation here would be a second policy.
  return <div className="artifact-canvas"><ArtifactView sessionId={sessionId} artifact={current.artifact} inCanvas /></div>;
}

type Fetched =
  | { sessionId: string; artifactId: string; artifact: ArtifactReference }
  | { sessionId: string; artifactId: string; message: string };
