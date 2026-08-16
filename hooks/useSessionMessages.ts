"use client";

import { useSessionStore } from "@/hooks/useSessionStore";

/**
 * Backward-compatible alias for useSessionStore. The data now lives in the
 * module-level SessionStore (residency), but the API surface is unchanged so
 * existing consumers (useChatController / useAgentSession) keep working.
 *
 * Phase A keeps the hook name as the integration seam; later phases can rename
 * call sites to useSessionStore directly.
 */
export function useSessionMessages(sessionId: string | null, activeBranchId?: string) {
  return useSessionStore(sessionId, activeBranchId);
}
