"use client";

import { useSyncExternalStore } from "react";

import {
  getWorkerReachability,
  probeWorkerReachability,
  subscribeWorkerReachability,
} from "@/lib/worker-api.client";

function subscribe(onChange: () => void): () => void {
  // Subscribing also starts the reachability monitor: the probe loop belongs to
  // the state it writes (see startReachabilityMonitor), so it runs for exactly
  // as long as someone is watching.
  const unsubscribe = subscribeWorkerReachability(onChange);
  // navigator.onLine only decides *when to ask again*. The answer that matters
  // comes from the Worker itself: a local-first app keeps working with no
  // network, so the browser's internet state must never be the verdict.
  const probe = () => { void probeWorkerReachability().catch(() => {}); };
  window.addEventListener("online", probe);
  window.addEventListener("offline", probe);
  return () => {
    unsubscribe();
    window.removeEventListener("online", probe);
    window.removeEventListener("offline", probe);
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
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
