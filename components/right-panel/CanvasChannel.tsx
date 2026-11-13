"use client";

import { PinOff } from "lucide-react";
import { useEffect, useState } from "react";

import { useT } from "@/components/LocaleProvider";
import { artifactMetadataURL, type ArtifactRecord } from "@/lib/artifacts";
import { apiFetch } from "@/lib/worker-api.client";

/**
 * The Canvas channel: the Project's pinned artifacts, newest pin first.
 *
 * Pin state is Worker state — the list is an aggregation over every Session in
 * the Project — so this channel fetches it fresh on every mount and holds it in
 * local state. Unpinning removes the card from the local list after the Worker
 * confirms the write; there is no shared store to keep in step, because the next
 * mount re-fetches the authoritative answer.
 */
export function CanvasChannel(props: {
  projectId: string | null;
  onOpen: (artifact: { sessionId: string; artifactId: string; name: string }) => void;
}) {
  const { projectId, onOpen } = props;
  const t = useT();
  const [state, setState] = useState<FetchState>({ kind: "loading" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (projectId === null) return;
    const controller = new AbortController();
    void apiFetch<PinnedList>(`/v1/projects/${encodeURIComponent(projectId)}/pinned-artifacts`, {
      signal: controller.signal,
    })
      .then((list) => {
        if (!controller.signal.aborted) {
          setState({ kind: "ready", artifacts: sortByPinned(list.artifacts ?? []) });
        }
      })
      .catch((reason: Error) => {
        // An abort means the channel was switched away or the Project changed;
        // it is not a failure anyone is waiting to read.
        if (!controller.signal.aborted) setState({ kind: "error", message: reason.message });
      });
    return () => controller.abort();
  }, [projectId, attempt]);

  // Both pin and unpin answer 200 with the refreshed artifact, not the list, so
  // the only local mutation is dropping the confirmed card; the next mount
  // re-fetches the authoritative set anyway.
  const unpin = async (artifact: ArtifactRecord) => {
    if (!artifact.sessionId) return;
    try {
      await apiFetch(`${artifactMetadataURL(artifact.sessionId, artifact.id)}/pin`, { method: "DELETE" });
      setState((current) => current.kind === "ready"
        ? { ...current, artifacts: current.artifacts.filter((entry) => entry.id !== artifact.id) }
        : current);
    } catch {
      // Leave the card: the Worker is the authority, and a failed write changes
      // nothing it will still answer with on the next fetch.
    }
  };

  // No Project selected yet: the channel has nothing it is allowed to ask for.
  if (projectId === null) return null;

  if (state.kind === "loading") {
    return <div className="right-panel-canvas-state" role="status">
      {t("rightPanel.canvas.loading")}
    </div>;
  }

  if (state.kind === "error") {
    return <div className="right-panel-canvas-state is-error" role="alert">
      {/* The sentence states what was observed; the cause goes underneath as the
          Worker said it. "The canvas is empty" would be a guess, and a wrong one
          for a network failure or a Worker restart. */}
      <span>{t("rightPanel.canvas.loadFailed")}</span>
      <span className="right-panel-canvas-detail" title={state.message}>{state.message}</span>
      <button type="button" className="artifact-widget-action" onClick={() => {
        setState({ kind: "loading" });
        setAttempt((value) => value + 1);
      }}>
        {t("rightPanel.canvas.retry")}
      </button>
    </div>;
  }

  if (state.artifacts.length === 0) {
    return <div className="right-panel-canvas-state" role="status">
      {t("rightPanel.canvas.empty")}
    </div>;
  }

  return (
    <ul className="right-panel-canvas-list">
      {state.artifacts.map((artifact) => (
        <CanvasCard
          key={`${artifact.sessionId ?? "unknown"}:${artifact.id}`}
          artifact={artifact}
          onOpen={onOpen}
          onUnpin={() => void unpin(artifact)}
        />
      ))}
    </ul>
  );
}

function CanvasCard({ artifact, onOpen, onUnpin }: {
  artifact: ArtifactRecord;
  onOpen: (artifact: { sessionId: string; artifactId: string; name: string }) => void;
  onUnpin: () => void;
}) {
  const t = useT();
  const sessionId = artifact.sessionId;
  const openLabel = t("rightPanel.canvas.open", { name: artifact.name });
  const unpinLabel = t("rightPanel.canvas.unpin", { name: artifact.name });
  const pinned = artifact.pinnedAt
    ? t("rightPanel.canvas.pinnedAt", { time: new Date(artifact.pinnedAt).toLocaleString() })
    : t("rightPanel.canvas.pinnedUndated");

  return (
    <li className="right-panel-canvas-card">
      <button
        type="button"
        className="right-panel-canvas-card-main"
        onClick={() => { if (sessionId) onOpen({ sessionId, artifactId: artifact.id, name: artifact.name }); }}
        disabled={!sessionId}
        aria-label={openLabel}
        title={openLabel}
      >
        <span className="right-panel-canvas-card-name">{artifact.name}</span>
        <span className="right-panel-canvas-card-meta">
          <span>{artifact.kind}</span>
          <span>{pinned}</span>
        </span>
      </button>
      {sessionId && (
        <button type="button" className="right-panel-canvas-unpin" onClick={onUnpin}
          aria-label={unpinLabel} title={unpinLabel}>
          <PinOff size={14} aria-hidden="true" />
        </button>
      )}
    </li>
  );
}

type FetchState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; artifacts: ArtifactRecord[] };

interface PinnedList {
  artifacts: ArtifactRecord[];
  skippedSessionCount?: number;
}

/**
 * Newest pin first. `pinnedAt` is optional in the record, so an undated pin
 * sorts after a dated one rather than crashing the comparison; the Worker
 * already returns the list sorted, this only makes the local order explicit.
 */
function sortByPinned(artifacts: ArtifactRecord[]): ArtifactRecord[] {
  return [...artifacts].sort((a, b) => {
    if (a.pinnedAt && b.pinnedAt) return b.pinnedAt.localeCompare(a.pinnedAt);
    if (a.pinnedAt) return -1;
    if (b.pinnedAt) return 1;
    return 0;
  });
}
