/** Shared formatting helpers for message chrome (clock + token counts). */

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

/**
 * Date-aware 24-hour clock for message timestamps, matching the dsh chrome:
 * same calendar day → `HH:mm`; earlier this year → `M/D HH:mm`; other years →
 * `YYYY/M/D HH:mm`.
 */
export function formatMessageClock(time: string | number, now: number = Date.now()): string {
  const d = new Date(time);
  if (Number.isNaN(d.getTime())) return "";
  const n = new Date(now);
  const clock = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  if (d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate()) {
    return clock;
  }
  const md = `${d.getMonth() + 1}/${d.getDate()}`;
  return d.getFullYear() === n.getFullYear() ? `${md} ${clock}` : `${d.getFullYear()}/${md} ${clock}`;
}

/** Humanized token count: `1.5k` / `2.3M`, whole numbers below 1000. */
export function formatTokenCount(tokens: number): string {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(1)}k`;
  return String(tokens);
}
