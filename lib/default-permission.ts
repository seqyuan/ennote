import type { PermissionMode } from "./permission-mode";

const STORAGE_KEY = "ennote-default-permission";

/** Dispatched on every write so live listeners (the composer) can adopt it immediately. */
export const DEFAULT_PERMISSION_EVENT = "ennote-default-permission-change";

const MODES: readonly PermissionMode[] = ["discuss", "ask", "auto"];

/** Read the persisted default permission for new sessions ("discuss" fallback). */
export function readDefaultPermissionMode(): PermissionMode {
  if (typeof window === "undefined") return "discuss";
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    if (stored !== null && (MODES as readonly string[]).includes(stored)) return stored as PermissionMode;
  } catch { /* unavailable storage: fall through to the default */ }
  return "discuss";
}

/** Persist the default permission for newly created sessions and broadcast it. */
export function writeDefaultPermissionMode(mode: PermissionMode): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, mode);
  } catch { /* unavailable storage: preference is best-effort */ }
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent<PermissionMode>(DEFAULT_PERMISSION_EVENT, { detail: mode }));
  }
}
