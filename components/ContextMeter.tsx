"use client";

/**
 * ContextMeter: the composer's context-occupancy ring beside the send button,
 * pixel-replicated from deepseek-harness's ui-conversation ContextMeter.
 *
 * It renders nothing until the Worker reports a projection (contextWindow +
 * projectedTokens), matching dsh's "renders nothing until a provider reports
 * both pressure and a route capacity". Clicking the ring opens the 264px
 * breakdown panel (percent + ~used/capacity + a stacked system/tools/messages
 * bar + legend rows).
 */
import { useEffect, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import type { SessionContextUsage } from "@/hooks/chat-controller-types";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";
import { formatTokens } from "@/lib/stats-format";

const RADIUS = 5.5;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

/** Marker the localized occupancy sentence is split on, so the panel headline
 * keeps its own tone while each locale owns the word order (dsh's READING_SLOT). */
const READING_SLOT = "\u0000";

const LEGACY_ROWS = [
  { key: "systemTokens", label: "context.system", color: "cm-color-system" },
  { key: "toolsTokens", label: "context.tools", color: "cm-color-tools" },
  { key: "messageTokens", label: "context.messages", color: "cm-color-messages" },
] as const;

const CATEGORY_ROWS: Record<string, { label: string; color: string }> = {
  system: { label: "context.system", color: "cm-color-system" },
  role: { label: "context.role", color: "cm-color-role" },
  rules: { label: "context.rules", color: "cm-color-rules" },
  skills: { label: "context.skills", color: "cm-color-skills" },
  tools: { label: "context.tools", color: "cm-color-tools" },
  reminders: { label: "context.reminders", color: "cm-color-reminders" },
  conversation: { label: "context.conversation", color: "cm-color-messages" },
};

interface MeterRow {
  key: string;
  label: string;
  color: string;
  tokens: number;
}

export function ContextMeter({ contextUsage, initialOpen = false }: {
  contextUsage: SessionContextUsage | null;
  /** Render the breakdown open on first paint (tests and deep links). */
  initialOpen?: boolean;
}) {
  const t = useT();
  const [open, setOpen] = useState(initialOpen);
  const rootRef = useRef<HTMLSpanElement | null>(null);

  // A model switch (or run end) can drop the projection while this component
  // stays mounted; close a now-unavailable panel instead of preserving stale UI.
  const available = contextUsage !== null;
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- a model switch can drop the projection; withdraw the stale panel
    if (!available && open) setOpen(false);
  }, [available, open]);

  useOverlayLayer(open && available, () => setOpen(false));

  // Outside click close (Menu's pattern).
  useEffect(() => {
    if (!open || !available) return;
    const onPointerDown = (event: PointerEvent) => {
      if (event.target instanceof Node && rootRef.current?.contains(event.target) === true) return;
      setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [available, open]);

  if (contextUsage === null) return null;
  if (contextUsage.contextWindow <= 0) return null;

  const percent = Math.min(100, Math.round(contextUsage.projectedTokens / contextUsage.contextWindow * 100));
  const reading = `${percent}%`;
  const aria = t("context.aria"); // "{percent} of context used" / "上下文已用 {percent}"
  const [headBefore = "", headAfter = ""] = aria
    .replace("{percent}", READING_SLOT)
    .split(READING_SLOT)
    .map((part) => part.trim());
  const categories = contextUsage.categories ?? [];
  const hasCategories = categories.length > 0 && categories.some((category) => category.tokens > 0);
  const breakdownTotal = hasCategories
    ? contextUsage.projectedTokens
    : contextUsage.systemTokens + contextUsage.toolsTokens + contextUsage.messageTokens;
  const rows: MeterRow[] = hasCategories
    ? categories.map((category) => ({
      key: category.key,
      label: CATEGORY_ROWS[category.key]?.label ?? "context.other",
      color: CATEGORY_ROWS[category.key]?.color ?? "cm-color-messages",
      tokens: category.tokens,
    }))
    : LEGACY_ROWS.map((row) => ({
      key: row.key,
      label: row.label,
      color: row.color,
      tokens: contextUsage[row.key],
    }));
  const parts = breakdownTotal === 0
    ? [{ key: "total", color: undefined, width: percent }]
    : rows.map((row) => ({
      key: row.key,
      color: row.color,
      width: contextUsage.contextWindow > 0
        ? row.tokens / contextUsage.contextWindow * 100
        : percent * row.tokens / breakdownTotal,
    }));
  const segments = parts.filter((part) => part.width > 0);

  return (
    <span ref={rootRef} className="context-meter">
      <button
        type="button"
        className="context-meter-trigger"
        aria-label={aria.replace("{percent}", reading)}
        aria-haspopup="dialog"
        aria-expanded={open}
        title={aria.replace("{percent}", reading)}
        onClick={() => setOpen((value) => !value)}
      >
        <svg viewBox="0 0 14 14" width="14" height="14" aria-hidden="true">
          <circle className="context-meter-track" cx="7" cy="7" r={RADIUS} />
          <circle
            className="context-meter-fill"
            cx="7"
            cy="7"
            r={RADIUS}
            strokeDasharray={`${CIRCUMFERENCE * percent / 100} ${CIRCUMFERENCE}`}
            transform="rotate(-90 7 7)"
          />
        </svg>
      </button>

      {open && (
        <div className="context-meter-panel" role="dialog" aria-label={t("context.used")}>
          <div className="context-meter-header">
            <span className="context-meter-headline">{headBefore}</span>
            <span className="context-meter-percent">{reading}</span>
            <span className="context-meter-headline">{headAfter}</span>
            <span className="context-meter-figures">
              {`~${formatTokens(contextUsage.projectedTokens)} / ${formatTokens(contextUsage.contextWindow)}`}
            </span>
          </div>
          <div className="context-meter-bar">
            {segments.map((segment) => (
              <div
                key={segment.key}
                className={`context-meter-segment${segment.color === undefined ? "" : ` ${segment.color}`}`}
                style={{ width: `${segment.width}%` }}
              />
            ))}
          </div>
          <dl className="context-meter-rows">
            {rows.map((row) => (
              <div key={row.key} className="context-meter-row">
                <dt>
                  <span className={`context-meter-swatch ${row.color}`} aria-hidden="true" />
                  {t(row.label)}
                </dt>
                <dd>
                  {`~${formatTokens(row.tokens)}`}
                  <span className="context-meter-row-pct">
                    {`${Math.round(row.tokens / contextUsage.contextWindow * 100)}%`}
                  </span>
                </dd>
              </div>
            ))}
          </dl>
        </div>
      )}
    </span>
  );
}
