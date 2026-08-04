"use client";

import { Loader2, TimerReset } from "lucide-react";
import { DelegationCompletionRow } from "@/components/DelegationCompletionRow";
import { useDelegationDelivery } from "@/hooks/useDelegationDelivery";

// BackgroundDelegationStrip sits above the Composer and shows the current
// session's background delegation work without blocking input. It is a pure
// projection: completion state comes from the durable handle/completion facts.
export function BackgroundDelegationStrip({ sessionId }: { sessionId: string | undefined }) {
  const { deliveries, refreshing } = useDelegationDelivery(sessionId);
  if (!deliveries.length) return null;
  const active = deliveries.filter(delivery => delivery.handle.status === "active");
  return <div className="background-delegation-strip" role="status" aria-label="Background delegation">
    <div className="background-delegation-header">
      <span><TimerReset size={13} aria-hidden="true" /> Background delegation</span>
      <span>{active.length ? `${active.length} active` : "all settled"}</span>
      {refreshing && <Loader2 size={12} aria-hidden="true" className="delivery-spinner" />}
    </div>
    <div className="delivery-list" role="list">
      {deliveries.map(delivery => delivery.completion
        ? <DelegationCompletionRow key={delivery.handle.id} completion={delivery.completion} />
        : <div className="delivery-row" key={delivery.handle.id} role="listitem">
            <span className="delivery-icon"><TimerReset size={13} aria-hidden="true" /></span>
            <span className="delivery-identity">
              <strong>Background delegation</strong>
              <span>{delivery.handle.executionMode} · {delivery.handle.status}</span>
            </span>
            <span className="delivery-summary">running in background…</span>
          </div>)}
    </div>
  </div>;
}
