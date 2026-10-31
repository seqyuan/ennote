"use client";

import { useId, useState } from "react";
import { Loader2, TimerReset } from "lucide-react";
import { DelegationCompletionRow, labeled } from "@/components/DelegationCompletionRow";
import { useDelegationDelivery, type BackgroundDelivery } from "@/hooks/useDelegationDelivery";
import { useT } from "@/components/LocaleProvider";

// BackgroundDelegationStrip sits above the Composer and shows the current
// session's background delegation work without blocking input. It is a pure
// projection: completion state comes from the durable handle/completion facts.
export function BackgroundDelegationStrip({ sessionId }: { sessionId: string | undefined }) {
  const { deliveries, refreshing } = useDelegationDelivery(sessionId);
  // Remount per session so a collapsed strip in one session never hides the
  // first completion of the next one.
  return <DelegationStrip key={sessionId ?? "no-session"} deliveries={deliveries} refreshing={refreshing} />;
}

export function DelegationStrip({ deliveries, refreshing }: {
  deliveries: BackgroundDelivery[];
  refreshing: boolean;
}) {
  const t = useT();
  const listId = useId();
  const completionCount = deliveries.filter(delivery => delivery.completion).length;
  const [state, setState] = useState(() => nextStripState({ seenCompletions: 0, collapsed: true }, completionCount));
  if (state.seenCompletions !== completionCount) setState(nextStripState(state, completionCount));
  if (!deliveries.length) return null;
  const open = !state.collapsed;
  const active = deliveries.filter(delivery => delivery.handle.status === "active");
  const summary = active.length
    ? t("conversation.delegation.active").replace("{count}", String(active.length))
    : t("conversation.delegation.settled");
  return <div className="background-delegation-strip" aria-label={t("conversation.delegation.background")}>
    <button type="button" className="background-delegation-header" aria-expanded={open} aria-controls={listId} onClick={() => setState(value => ({ ...value, collapsed: !value.collapsed }))}>
      <span>
        <TimerReset size={13} aria-hidden="true" /> {t("conversation.delegation.background")}
        {refreshing && <Loader2 size={12} aria-hidden="true" className="delivery-spinner" />}
      </span>
      {/* The visual summary stays inert for assistive tech; the live region
          below announces it, so the toggle is not wrapped in role="status". */}
      <span aria-hidden="true">{summary}</span>
    </button>
    <span className="sr-only" role="status" aria-live="polite">{summary}</span>
    {open && <div className="delivery-list" id={listId} role="list">
      {deliveries.map(delivery => delivery.completion
        ? <DelegationCompletionRow key={delivery.handle.id} completion={delivery.completion} />
        : <div className="delivery-row" key={delivery.handle.id} role="listitem">
            <span className="delivery-icon"><TimerReset size={13} aria-hidden="true" /></span>
            <span className="delivery-identity">
              <strong>{labeled(t, "delegation.mode", delivery.handle.executionMode)} · {labeled(t, "delegation.handle", delivery.handle.status)}</strong>
            </span>
            <span className="delivery-summary">{t("conversation.delegation.running")}</span>
          </div>)}
    </div>}
  </div>;
}

/**
 * Expansion rule for the strip.
 *
 * - Active-only work is ambient, so a freshly mounted strip stays collapsed.
 * - A durable completion is news, so the first render after a reload with an
 *   existing completion is open.
 * - An explicit user collapse wins while the completion count is unchanged; a
 *   completion that arrives afterwards re-opens the strip once, so the fact is
 *   never hidden behind a guess.
 */
export function nextStripState(
  previous: { seenCompletions: number; collapsed: boolean },
  completionCount: number,
): { seenCompletions: number; collapsed: boolean } {
  if (completionCount === previous.seenCompletions) return previous;
  return {
    seenCompletions: completionCount,
    collapsed: completionCount > previous.seenCompletions ? false : previous.collapsed,
  };
}
