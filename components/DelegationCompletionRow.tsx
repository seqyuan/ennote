"use client";

import { Bot, CircleAlert, CircleCheck, Clock3 } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import type { components } from "@/lib/worker-api.gen";

type DelegationCompletion = components["schemas"]["DelegationCompletion"];

function boundedResult(result?: Record<string, unknown>): string {
  if (!result) return "";
  const text = typeof result === "string" ? result : JSON.stringify(result);
  if (text.length > 300) return text.slice(0, 300) + "…";
  return text;
}

// DelegationCompletionRow renders one background delegation delivery as a
// compact, read-only row. The completion payload is the bounded public summary;
// the private transcript stays behind the child Run endpoint.
export function DelegationCompletionRow({ completion }: { completion: DelegationCompletion }) {
  const t = useT();
  const kind = completion.kind ?? "completed";
  const status = completion.deliveryStatus ?? "pending";
  const active = status === "pending" || status === "resume_queued";
  const icon = kind === "cancelled" ? <CircleAlert size={13} aria-hidden="true" />
    : active ? <Clock3 size={13} aria-hidden="true" />
    : <CircleCheck size={13} aria-hidden="true" />;
  return <div className="delivery-row" data-delivery-status={status} role="listitem">
    <span className="delivery-icon">{icon}</span>
    <span className="delivery-identity">
      <strong>{t("delegation.completion").replace("{generation}", String(completion.generation ?? ""))}</strong>
      <span>{labeled(t, "delegation.handle", status)}</span>
    </span>
    <span className="delivery-summary"><Bot size={11} aria-hidden="true" />{boundedResult(completion.result)}</span>
  </div>;
}

// Translate a delegation enum value that may not have copy yet: an unknown
// value is humanized rather than echoing the raw wire code. Shared with the
// strip (which imports this module, so the helper lives here).
export function labeled(t: (key: string) => string, prefix: string, value: string): string {
  const key = `${prefix}.${value}`;
  const text = t(key);
  return text === key ? value.replaceAll("_", " ") : text;
}
