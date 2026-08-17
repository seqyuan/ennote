/** Shared compact formatting for the composer StatsLine and ContextMeter,
 *  matching deepseek-harness's StatsLine helpers. */

/** 517 / 12.2K / 517K / 1.2M (one decimal under three digits). */
export function formatTokens(n: number): string {
  const scaled = (v: number): string => (v >= 100 ? String(Math.round(v)) : String(Math.round(v * 10) / 10));
  if (n < 1_000) return String(n);
  if (n < 1_000_000) return `${scaled(n / 1_000)}K`;
  return `${scaled(n / 1_000_000)}M`;
}

/** 45.2s under a minute, 2m42s from there on. */
export function formatDuration(ms: number): string {
  const s = ms / 1_000;
  if (s < 60) return `${Math.round(s * 10) / 10}s`;
  const whole = Math.round(s);
  return `${Math.floor(whole / 60)}m${whole % 60}s`;
}

/** Whole tokens per second from ten up, one decimal below. */
export function formatTokensPerSecond(tps: number): string {
  const clamped = Math.max(0, tps);
  return clamped >= 10 ? String(Math.round(clamped)) : String(Math.round(clamped * 10) / 10);
}

/** Sub-ten-second latency with one decimal (0.6), whole seconds beyond (12). */
export function formatLatencySeconds(ms: number): string {
  const s = Math.max(0, ms) / 1000;
  return s < 10 ? String(Math.round(s * 10) / 10) : String(Math.round(s));
}
