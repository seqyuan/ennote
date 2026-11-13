"use client";

import { useCallback } from "react";
import { nextFreePane } from "@/lib/inspector-tabs";
import { useOpenArtifactIntent } from "@/hooks/useOpenArtifactIntent";
import type { useRightPanel } from "@/hooks/useRightPanel";

export function useCanvasArtifactIntent(
  selectedSession: string | null,
  panel: ReturnType<typeof useRightPanel>,
): void {
  // The transcript asks for the canvas through an event rather than a prop: the
  // button sits five components below the state that can satisfy it. The hook
  // drops intents from any session but the one on screen; the pane is the free
  // half, so a second canvas sits beside the first instead of replacing it.
  const { openArtifact, state: panelState } = panel;
  useOpenArtifactIntent(selectedSession, useCallback(
    (intent) => openArtifact(intent, nextFreePane(panelState.activePanes)),
    [openArtifact, panelState.activePanes],
  ));
}
