"use client";

import { useEffect, useSyncExternalStore } from "react";

import {
  getWorkerReachability,
  probeWorkerReachability,
  subscribeWorkerReachability,
} from "@/lib/worker-api.client";

/** How often an unreachable Worker is re-probed while the banner is showing. */
const UNREACHABLE_POLL_MS = 5_000;

function subscribe(onChange: () => void): () => void {
  const unsubscribe = subscribeWorkerReachability(onChange);
  // navigator.onLine only decides *when to ask again*. The answer that matters
  // comes from the Worker itself: a local-first app keeps working with no
  // network, so the browser's internet state must never be the verdict.
  const probe = () => { void probeWorkerReachability(); };
  window.addEventListener("online", probe);
  window.addEventListener("offline", probe);
  const timer = window.setInterval(() => {
    if (getWorkerReachability() === "unreachable") probe();
  }, UNREACHABLE_POLL_MS);
  return () => {
    unsubscribe();
    window.removeEventListener("online", probe);
    window.removeEventListener("offline", probe);
    window.clearInterval(timer);
  };
}

function getSnapshot(): boolean {
  return getWorkerReachability() === "unreachable";
}

// The server has no Worker to probe: paint as reachable and let the first client
// snapshot correct it, so hydration stays deterministic.
function getServerSnapshot(): boolean {
  return false;
}

/**
 * Whether the App is in read-only Offline state (AGENTS.md), decided by the
 * Worker's reachability rather than by the browser's online flag.
 */
export function useWorkerReachability(): boolean {
  const unreachable = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
  // Probe on mount only when the browser already reports itself offline: that is
  // the one case worth checking before the user acts. A probe while the browser
  // claims to be online would put a request on every page load for a state the
  // first real exchange answers anyway.
  useEffect(() => {
    if (getWorkerReachability() === "unknown" && navigator.onLine === false) {
      void probeWorkerReachability();
    }
  }, []);
  return unreachable;
}
