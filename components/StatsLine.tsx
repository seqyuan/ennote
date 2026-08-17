"use client";

/**
 * StatsLine: the pipe-separated stats strip below the composer, ported from
 * deepseek-harness's ui-conversation StatsLine. It renders the session's
 * aggregate turn/step counts, model/tool wall time, first-token/decode
 * throughput, cache-hit share, and input/output token billing — all computed
 * Worker-side from durable model_calls/tool_calls so paging and compaction
 * cannot change them.
 */
import { useLayoutEffect, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import type { SessionStats } from "@/hooks/chat-controller-types";
import { formatDuration, formatTokens, formatTokensPerSecond } from "@/lib/stats-format";

function fill(template: string, vars: Record<string, string>): string {
  return template.replace(/\{(\w+)\}/g, (_, name: string) => vars[name] ?? `{${name}}`);
}

export function StatsLine({ stats }: { stats: SessionStats | null }) {
  const t = useT();

  const groups: string[] = [];
  if (stats !== null) {
    if (stats.steps > 0) {
      groups.push(fill(t("stats.counts"), { turns: String(stats.turns), steps: String(stats.steps) }));
      const durations: string[] = [];
      if (stats.llmMs > 0) durations.push(fill(t("stats.llm"), { duration: formatDuration(stats.llmMs) }));
      if (stats.toolMs > 0) durations.push(fill(t("stats.toolCall"), { duration: formatDuration(stats.toolMs) }));
      if (durations.length > 0) groups.push(durations.join(" · "));
      const speeds: string[] = [];
      if (stats.ttftSteps > 0) {
        speeds.push(fill(t("stats.ttftAverage"), { duration: formatDuration(stats.ttftMs / stats.ttftSteps) }));
      }
      if (stats.decodeMs > 0) {
        speeds.push(fill(t("stats.tokensPerSecond"), {
          throughput: formatTokensPerSecond(stats.decodeTokens / (stats.decodeMs / 1_000)),
        }));
      }
      if (speeds.length > 0) groups.push(speeds.join(" · "));
    }

    const billedInput = stats.uncachedInputTokens + stats.cacheReadTokens + stats.cacheWriteTokens;
    if (billedInput > 0 || stats.outputTokens > 0) {
      if (billedInput > 0) {
        groups.push(fill(t("stats.cacheHit"), { percent: String(Math.round(stats.cacheReadTokens / billedInput * 100)) }));
      }
      groups.push(fill(t("stats.tokens"), {
        input: formatTokens(billedInput),
        output: formatTokens(stats.outputTokens),
      }));
    }
  }

  const line = groups.join(" | ");
  const rootRef = useRef<HTMLDivElement | null>(null);
  const [truncated, setTruncated] = useState(false);
  useLayoutEffect(() => {
    const el = rootRef.current;
    if (el === null) return;
    const measure = () => { setTruncated(el.scrollWidth > el.clientWidth); };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => { observer.disconnect(); };
  }, [line]);

  if (groups.length === 0) return null;
  return (
    <div ref={rootRef} className="stats-line" title={truncated ? line : undefined}>
      {groups.map((group, index) => (
        <span key={group}>
          {index > 0 && (<><span className="stats-line-sep" aria-hidden="true">|</span> </>)}
          <span>{group}</span>
        </span>
      ))}
    </div>
  );
}
