"use client";

import { useSessionStore } from "@/hooks/useSessionStore";
import type { ModelResolver } from "@/lib/chat-messages";

/**
 * Backward-compatible alias for useSessionStore. The data now lives in the
 * module-level SessionStore (residency), but the API surface is unchanged so
 * existing consumers (useChatController / useAgentSession) keep working.
 *
 * Phase A keeps the hook name as the integration seam; later phases can rename
 * call sites to useSessionStore directly.
 */
export function useSessionMessages(
  sessionId: string | null,
  activeBranchId?: string,
  options?: { resolveModel?: ModelResolver },
) {
  return useSessionStore(sessionId, activeBranchId, options);
}
