"use client";

import { AlertTriangle, ExternalLink, RotateCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { artifactDownloadURL, artifactPreviewURL, type ArtifactReference } from "@/lib/artifacts";
import {
  WIDGET_FRAME_SANDBOX, WIDGET_MESSAGE_THEME, WIDGET_READY_TIMEOUT_MS,
  forgetWidgetHeight, isWidgetReady, parseWidgetError, parseWidgetHeight,
  readCachedWidgetHeight, rememberWidgetHeight, type WidgetFrameError,
} from "@/lib/widget-frame";
import { widgetThemeVarsFromDocument } from "@/lib/widget-theme";

/**
 * Renders a `widget` artifact.
 *
 * The frame document, the CSP and the receiver script all come from the Worker;
 * this component supplies only what only the client knows — the `sandbox`
 * attribute, the theme values, and how tall the frame is allowed to be in a
 * conversation. If a widget fails, the failure stays inside this box: nothing here
 * can unmount the message or the panels around it.
 */
export function ArtifactWidget({ sessionId, artifact }: { sessionId: string; artifact: ArtifactReference }) {
  const t = useT();
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const [attempt, setAttempt] = useState(0);
  // A remount (message collapsed and expanded) starts at the height it last reported
  // instead of growing into it on screen.
  const [height, setHeight] = useState(() => readCachedWidgetHeight(artifact.artifactId));
  const [tweened, setTweened] = useState(() => readCachedWidgetHeight(artifact.artifactId) > 0);
  const tweenedRef = useRef(tweened);
  const [ready, setReady] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [failure, setFailure] = useState<WidgetFrameError | null>(null);
  const [timedOut, setTimedOut] = useState(false);

  // Theme values are read from the live document, not baked in: the same widget has
  // to follow a light/dark switch without rebuilding the frame.
  const pushTheme = useCallback(() => {
    const frame = iframeRef.current?.contentWindow;
    if (!frame) return;
    try {
      frame.postMessage({ type: WIDGET_MESSAGE_THEME, vars: widgetThemeVarsFromDocument() }, "*");
    } catch {
      // A frame that has gone away cannot receive a theme; the next mount will.
    }
  }, []);

  useEffect(() => {
    function onMessage(event: MessageEvent) {
      const frame = iframeRef.current?.contentWindow;
      if (!frame || event.source !== frame) return;
      const payload = event.data;
      if (isWidgetReady(payload)) {
        setReady(true);
        setTimedOut(false);
        pushTheme();
        return;
      }
      const nextHeight = parseWidgetHeight(payload);
      if (nextHeight !== null) {
        setHeight(nextHeight);
        rememberWidgetHeight(artifact.artifactId, nextHeight);
        // Only the first report skips the transition; later ones are refinements.
        if (!tweenedRef.current) {
          tweenedRef.current = true;
          setTweened(true);
        }
        return;
      }
      const nextFailure = parseWidgetError(payload);
      if (nextFailure) setFailure(current => current ?? nextFailure);
    }
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [artifact.artifactId, pushTheme]);

  // A theme switch is a class change on <html>; that is the whole signal the app
  // exposes, so it is the one we watch.
  useEffect(() => {
    if (typeof MutationObserver === "undefined" || !ready) return;
    const observer = new MutationObserver(pushTheme);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "data-theme"] });
    return () => observer.disconnect();
  }, [pushTheme, ready]);

  // The clock starts when the document reports having loaded, not when the component
  // mounted. A frame below the fold has not failed to answer — it has not been asked
  // yet — and a CDN script at the end of the body legitimately delays `ready` past a
  // mount-anchored timer. `loaded` is set by the frame's own load event.
  useEffect(() => {
    if (ready || !loaded) return;
    const timer = setTimeout(() => setTimedOut(true), WIDGET_READY_TIMEOUT_MS);
    return () => clearTimeout(timer);
  }, [attempt, loaded, ready]);

  const previewURL = useMemo(() => artifactPreviewURL(sessionId, artifact.artifactId), [artifact.artifactId, sessionId]);
  const downloadURL = artifactDownloadURL(sessionId, artifact.artifactId);

  const retry = useCallback(() => {
    setAttempt(value => value + 1);
    // Retry means starting over, including the height: keeping the old one would make
    // a collapsed-then-expanded frame come back at a size from before the retry.
    forgetWidgetHeight(artifact.artifactId);
    setHeight(0);
    tweenedRef.current = false;
    setTweened(false);
    setReady(false);
    setLoaded(false);
    setFailure(null);
    setTimedOut(false);
  }, [artifact.artifactId]);

  // `loading` covers "not loaded yet", `degraded` a script or dependency failure,
  // `stalled` a document that loaded and then said nothing. A stalled frame is kept in
  // the document rather than replaced: `no-store` means a retry is a real reload, and a
  // late `ready` arrives on the frame that is already there.
  const state = failure ? "degraded" : timedOut && !ready ? "stalled" : ready ? "ready" : "loading";

  return <div className="artifact-widget" data-widget-state={state}>
    {timedOut && !ready && <div className="artifact-widget-notice" role="alert">
      <span>{t("conversation.artifact.widget.timeout")}</span>
      <span className="artifact-widget-actions">
        <button type="button" className="artifact-widget-action" onClick={retry}>
          <RotateCw size={13} aria-hidden="true" />{t("conversation.artifact.widget.retry")}
        </button>
        <a className="artifact-widget-action" href={downloadURL} download={artifact.name}>
          <ExternalLink size={13} aria-hidden="true" />{t("conversation.artifact.widget.viewSource")}
        </a>
      </span>
    </div>}
    {failure && <div className="artifact-widget-notice">
      <AlertTriangle size={13} aria-hidden="true" />
      <span role="status" title={failure.detail || undefined}>{failure.kind === "csp"
        ? t("conversation.artifact.widget.externalBlocked")
        : t("conversation.artifact.widget.scriptError")}</span>
      <span className="artifact-widget-actions">
        <a className="artifact-widget-action" href={downloadURL} download={artifact.name}>
          {t("conversation.artifact.widget.viewSource")}
        </a>
        <button type="button" className="artifact-widget-action" onClick={retry}>
          {t("conversation.artifact.widget.retry")}
        </button>
      </span>
    </div>}
    <iframe
      key={attempt}
      ref={iframeRef}
      className="artifact-widget-frame"
      // Not `data-artifact-id`: that marks an artifact row, and every spec that
      // counts rows queries it. A frame is a frame.
      data-widget-artifact={artifact.artifactId}
      data-widget-attempt={attempt}
      src={previewURL}
      title={t("conversation.artifact.widget.aria", { name: artifact.name })}
      // allow-scripts and nothing else. Adding allow-same-origin next to it would let
      // the frame reach parent.document and strip its own sandbox, which is the same
      // as having none.
      sandbox={WIDGET_FRAME_SANDBOX}
      referrerPolicy="no-referrer"
      loading="lazy"
      onLoad={() => setLoaded(true)}
      style={{
        height: height > 0 ? `${height}px` : undefined,
        transition: tweened ? "height var(--motion-base) var(--ease-standard)" : "none",
      }}
    />
  </div>;
}
