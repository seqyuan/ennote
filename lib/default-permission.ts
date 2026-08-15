import type { PermissionMode } from "./permission-mode";

const STORAGE_KEY = "ennote-default-permission";

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

/** Persist the default permission for newly created sessions. */
export function writeDefaultPermissionMode(mode: PermissionMode): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, mode);
  } catch { /* unavailable storage: preference is best-effort */ }
}
