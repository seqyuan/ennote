"use client";

import { GraphActivityPanel } from "../GraphActivityPanel";

/**
 * The Graphs channel: this session's Agent Flow runs, newest first.
 *
 * The panel polls while a run is live, so it is mounted only while this channel
 * is the one on screen.
 */
export function GraphsChannel({ sessionId, onCount }: {
  sessionId: string | null;
  onCount?: (count: number) => void;
}) {
  return <GraphActivityPanel sessionId={sessionId} onCount={onCount} />;
}
