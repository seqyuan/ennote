"use client";

import { useState, type ReactNode } from "react";
import { useT } from "@/components/LocaleProvider";
import { shouldCollapseMessage } from "@/lib/message-collapse";

/**
 * Bounds a long user message. The body keeps the full Markdown tree; only the
 * viewport is clipped, so copy and fork continue to use `turn.user.text`.
 */
export function CollapsibleMessage({ text, children }: { text: string; children: ReactNode }) {
  const t = useT();
  const collapsible = shouldCollapseMessage(text);
  const [expanded, setExpanded] = useState(false);
  const collapsed = collapsible && !expanded;

  return (
    <div className="collapsible-message" data-collapsed={collapsed || undefined}>
      <div className="collapsible-message-body">{children}</div>
      {collapsible && (
        <button
          type="button"
          className="collapsible-message-toggle"
          aria-expanded={expanded}
          onClick={() => setExpanded((value) => !value)}
        >
          {t(expanded ? "message.collapse" : "message.expand")}
        </button>
      )}
    </div>
  );
}
