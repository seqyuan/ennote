"use client";

import { useSyncExternalStore } from "react";

/**
 * The browser's offline flag, the same signal `apiResponse` uses to refuse
 * mutations (lib/worker-api.client.ts). Sharing one signal keeps the read-only
 * banner and the guard from disagreeing.
 */
function subscribe(onChange: () => void): () => void {
  window.addEventListener("online", onChange);
  window.addEventListener("offline", onChange);
  return () => {
    window.removeEventListener("online", onChange);
    window.removeEventListener("offline", onChange);
  };
}

function getSnapshot(): boolean {
  return navigator.onLine === false;
}

// The server has no navigator: paint as online and let the first client
// snapshot correct it, so hydration stays deterministic.
function getServerSnapshot(): boolean {
  return false;
}

export function useOfflineStatus(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
