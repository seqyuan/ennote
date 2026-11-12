"use client";

import { useEffect, useLayoutEffect, useRef } from "react";

import { subscribeOpenArtifact, type OpenArtifactIntent } from "@/lib/canvas-intent";

/** `useLayoutEffect` on the client, `useEffect` during a server render (no warning). */
const useIsomorphicLayoutEffect = typeof window === "undefined" ? useEffect : useLayoutEffect;

/**
 * Honour "open in the canvas" requests made from the message timeline.
 *
 * Two things have to be true at the moment the event fires rather than when the
 * effect ran, so both live in a ref: the session on screen (an intent from the
 * session that was just left must not reach the lane — AGENTS.md scopes every
 * event by its owner) and the handler (which closes over the panel state and is
 * a new function most renders). Subscribing once and reading the current values
 * avoids a resubscribe that would open a gap for a dropped click.
 */
export function useOpenArtifactIntent(
  activeSessionId: string | null,
  onOpen: (intent: OpenArtifactIntent) => void,
): void {
  const latest = useRef({ activeSessionId, onOpen });
  // A layout effect, not a passive one: both fields must be from the same render, and
  // a passive effect runs after paint — in that window the stored session can already
  // be the old one while the DOM shows the new one, so a click there would pass the
  // check below and hand the panel a callback bound to the Project that was just
  // left. Layout effects settle before the browser paints, so nothing the reader can
  // click was committed before this assignment. (Assigning during render would be
  // tighter still, and this repo's lint forbids touching a ref while rendering.)
  useIsomorphicLayoutEffect(() => {
    latest.current = { activeSessionId, onOpen };
  }, [activeSessionId, onOpen]);

  useEffect(() => subscribeOpenArtifact((intent) => {
    // A late intent and an intent from another Project look the same here, and
    // neither may put its artifact in the lane: the row would name a session the
    // panel cannot read.
    if (intent.sessionId !== latest.current.activeSessionId) return;
    latest.current.onOpen(intent);
  }), []);
}
