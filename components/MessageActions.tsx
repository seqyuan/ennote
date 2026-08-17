"use client";

import { Check, Copy, GitFork } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { formatMessageClock } from "@/lib/message-chrome";
import { formatLatencySeconds, formatTokensPerSecond } from "@/lib/stats-format";

interface MessageActionsProps {
  /** Plain text copied by the copy action. */
  text: string;
  /** ISO timestamp for the clock; omitted for transient messages. */
  time?: string | null;
  /** Turn wall time in ms, appended to the clock as `· Ran for 1s`. */
  runMs?: number;
  /** Turn first-step TTFT in ms, appended as `· TTFT 0.6s`. */
  ttftMs?: number;
  /** Turn decode throughput, appended as `· 101 tok/s`. */
  tokensPerSecond?: number;
  /** When set, a fork action branches from this message. */
  branchMessageId?: string;
  /** Gray out the fork action (run active or branch change in flight). */
  branchDisabled?: boolean;
  onBranch?: (messageId: string) => void;
}

function fill(template: string, vars: Record<string, string>): string {
  return template.replace(/\{(\w+)\}/g, (_, name: string) => vars[name] ?? `{${name}}`);
}

/** Whole-seconds run duration ("1s" / "2m03s"), localized through the locale seat. */
function formatRunDuration(ms: number, t: (key: string) => string): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  if (minutes > 0) {
    return fill(t("duration.minutes"), { minutes: String(minutes), seconds: String(seconds).padStart(2, "0") });
  }
  return fill(t("duration.seconds"), { seconds: String(seconds) });
}

/** Copy / fork (/ clock + turn metrics) action row shared by user and assistant messages. */
export function MessageActions({ text, time, runMs, ttftMs, tokensPerSecond, branchMessageId, branchDisabled, onBranch }: MessageActionsProps) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const copy = useCallback(() => {
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1000);
    }).catch(() => { /* clipboard unavailable in non-secure contexts */ });
  }, [text]);

  const clock = time ? formatMessageClock(time) : null;
  // The dot is decorative; flanking spaces keep the readings separate for a screen reader.
  const readings: string[] = [];
  if (runMs !== undefined) readings.push(fill(t("message.ranFor"), { duration: formatRunDuration(runMs, t) }));
  if (ttftMs !== undefined) readings.push(fill(t("message.ttft"), { seconds: formatLatencySeconds(ttftMs) }));
  if (tokensPerSecond !== undefined) readings.push(fill(t("message.tokensPerSecond"), { tps: formatTokensPerSecond(tokensPerSecond) }));
  const clockLine = [clock, ...readings].filter(Boolean).join(" · ");

  const showBranch = Boolean(branchMessageId && onBranch);
  if (!clockLine && !showBranch && !text) return null;

  return <div className="message-actions">
    {clockLine && <span className="message-clock">{clockLine}</span>}
    <button type="button" className="message-action" aria-label={t(copied ? "copied" : "copy")}
      title={t(copied ? "copied" : "copy")} onClick={copy}>
      {copied ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
    </button>
    {showBranch && (
      <button type="button" className="message-action" aria-label={t("message.branch")}
        title={t("message.branch")} disabled={branchDisabled}
        onClick={() => onBranch!(branchMessageId!)}>
        <GitFork size={13} aria-hidden="true" />
      </button>
    )}
  </div>;
}
