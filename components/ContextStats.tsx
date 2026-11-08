"use client";

/**
 * ContextStats: the session's aggregate figures, rendered inside the context
 * meter's breakdown panel. They used to live in their own pipe-separated strip
 * under the composer, which put a second numeric row on screen next to the ring
 * that already reports the context budget — the strip is gone and these numbers
 * now open from the ring they belong beside.
 *
 * Everything is computed Worker-side from durable model_calls/tool_calls, so
 * paging and compaction cannot change it.
 */
import { useT } from "@/components/LocaleProvider";
import type { SessionStats } from "@/hooks/chat-controller-types";
import type { Translator } from "@/lib/messages";
import { formatDuration, formatTokens, formatTokensPerSecond } from "@/lib/stats-format";

function fill(template: string, vars: Record<string, string>): string {
  return template.replace(/\{(\w+)\}/g, (_, name: string) => vars[name] ?? `{${name}}`);
}

/** One entry per rendered line; the key is stable across rerenders. */
export function sessionStatLines(stats: SessionStats | null, t: Translator): string[] {
  if (stats === null) return [];
  const lines: string[] = [];
  if (stats.steps > 0) {
    lines.push(fill(t("stats.counts"), { turns: String(stats.turns), steps: String(stats.steps) }));
    const durations: string[] = [];
    if (stats.llmMs > 0) durations.push(fill(t("stats.llm"), { duration: formatDuration(stats.llmMs) }));
    if (stats.toolMs > 0) durations.push(fill(t("stats.toolCall"), { duration: formatDuration(stats.toolMs) }));
    if (durations.length > 0) lines.push(durations.join(" · "));
    const speeds: string[] = [];
    if (stats.ttftSteps > 0) {
      speeds.push(fill(t("stats.ttftAverage"), { duration: formatDuration(stats.ttftMs / stats.ttftSteps) }));
    }
    if (stats.decodeMs > 0) {
      speeds.push(fill(t("stats.tokensPerSecond"), {
        throughput: formatTokensPerSecond(stats.decodeTokens / (stats.decodeMs / 1_000)),
      }));
    }
    if (speeds.length > 0) lines.push(speeds.join(" · "));
  }

  const billedInput = stats.uncachedInputTokens + stats.cacheReadTokens + stats.cacheWriteTokens;
  if (billedInput > 0) {
    lines.push(fill(t("stats.cacheHit"), { percent: String(Math.round(stats.cacheReadTokens / billedInput * 100)) }));
  }
  if (billedInput > 0 || stats.outputTokens > 0) {
    lines.push(fill(t("stats.tokens"), {
      input: formatTokens(billedInput),
      output: formatTokens(stats.outputTokens),
    }));
  }
  return lines;
}

export function ContextStats({ stats }: { stats: SessionStats | null }) {
  const t = useT();
  const lines = sessionStatLines(stats, t);
  if (lines.length === 0) return null;
  return (
    <div className="context-meter-stats">
      <span className="context-meter-stats-label">{t("stats.section")}</span>
      {lines.map((line) => <span key={line}>{line}</span>)}
    </div>
  );
}
