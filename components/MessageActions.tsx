"use client";

import { Check, Copy, GitFork } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { formatMessageClock } from "@/lib/message-chrome";

interface MessageActionsProps {
  /** Plain text copied by the copy action. */
  text: string;
  /** ISO timestamp for the clock; omitted for transient messages. */
  time?: string | null;
  /** When set, a fork action branches from this message. */
  branchMessageId?: string;
  /** Gray out the fork action (run active or branch change in flight). */
  branchDisabled?: boolean;
  onBranch?: (messageId: string) => void;
}

/** Copy / fork (/ clock) action row shared by user and assistant messages. */
export function MessageActions({ text, time, branchMessageId, branchDisabled, onBranch }: MessageActionsProps) {
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
  const showBranch = Boolean(branchMessageId && onBranch);
  if (!clock && !showBranch && !text) return null;

  return <div className="message-actions">
    {clock && <span className="message-clock">{clock}</span>}
    <button type="button" className="message-action" aria-label={copied ? "Copied" : "Copy"}
      title={copied ? "Copied" : "Copy"} onClick={copy}>
      {copied ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
    </button>
    {showBranch && (
      <button type="button" className="message-action" aria-label="Branch from this message"
        title="Branch from this message" disabled={branchDisabled}
        onClick={() => onBranch!(branchMessageId!)}>
        <GitFork size={13} aria-hidden="true" />
      </button>
    )}
  </div>;
}
